package mailbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/controlscope"
)

type Service struct {
	Store         *Store
	port          Port
	root          string
	maxHops       int
	ctx           context.Context
	mu            sync.Mutex
	chatLocks     map[string]*chatLock
	scopes        map[string]controlscope.Scope
	notifications contracts.NotificationSink
	wg            sync.WaitGroup
	closed        bool
	prepared      map[string]*preparedMail
}

// chatLock is dropped when no dispatch for its Chat is pending.
type chatLock struct {
	mu   sync.Mutex
	refs int
}

type preparedMail struct {
	digest string
	value  Prepared
	err    error
	done   chan struct{}
}

func New(ctx context.Context, store *Store, root string, maxHops int, notifications contracts.NotificationSink) *Service {
	if maxHops <= 0 {
		maxHops = 4
	}
	return &Service{Store: store, root: root, maxHops: maxHops, ctx: ctx, chatLocks: map[string]*chatLock{}, scopes: map[string]controlscope.Scope{}, notifications: notifications}
}
func (s *Service) Bind(port Port) { s.port = port }
func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + fmt.Sprintf("%013x", now()) + hex.EncodeToString(b[:])
}
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.wg.Wait()
	return s.Store.Close()
}
func (s *Service) Prepare(ctx context.Context, req Request) (Prepared, error) {
	key := req.Start.Origin.RunID + "\x00" + req.Start.Origin.ToolID
	d := digest(req.Start.Input)
	s.mu.Lock()
	if s.prepared == nil {
		s.prepared = map[string]*preparedMail{}
	}
	entry, exists := s.prepared[key]
	if !exists {
		entry = &preparedMail{digest: d, done: make(chan struct{})}
		s.prepared[key] = entry
	}
	s.mu.Unlock()
	if entry.digest != d {
		return Prepared{}, failure("idempotency_conflict", "mail invocation arguments changed")
	}
	if !exists {
		entry.value, entry.err = s.port.PrepareMail(ctx, req)
		close(entry.done)
	}
	select {
	case <-entry.done:
		return entry.value, entry.err
	case <-ctx.Done():
		return Prepared{}, ctx.Err()
	}
}
func (s *Service) MaxHops() int { return s.maxHops }
func digest(input any) string {
	h := sha256.Sum256([]byte(jsonText(input)))
	return hex.EncodeToString(h[:])
}
func (s *Service) setScope(id string, scope controlscope.Scope) {
	s.mu.Lock()
	s.scopes[id] = scope
	s.mu.Unlock()
}
func (s *Service) scope(id string) (controlscope.Scope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.scopes[id]
	return v, ok
}
func (s *Service) dropScope(id string) {
	s.mu.Lock()
	delete(s.scopes, id)
	s.mu.Unlock()
}

// change commits one row transition and publishes it. A callback returning
// errStale leaves the row untouched.
func (s *Service) change(id string, fn func(*Mail) error) (Mail, error) {
	m, err := s.Store.Change(id, fn)
	if err == nil {
		s.changed(m)
	}
	return m, err
}

// changed publishes a transition and forgets the in-process delivery identity
// once the row can never be delivered again.
func (s *Service) changed(m Mail) {
	if m.Terminal() {
		s.dropScope(m.ID)
	}
	if s.notifications != nil {
		s.notifications.Broadcast("mail.updated", map[string]any{"mailId": m.ID, "kind": m.Kind, "status": m.Status, "delivery": m.Delivery, "toAgentKey": m.ToAgentKey, "toChatId": m.ToChatID, "runId": m.RunID})
	}
}
func (s *Service) all(f Filter) ([]Mail, error) {
	f.Limit = 1000
	var out []Mail
	for {
		rows, err := s.Store.List(f)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < f.Limit {
			return out, nil
		}
		f.Offset += len(rows)
	}
}
func (s *Service) Send(ctx context.Context, req Request) (Receipt, error) {
	if req.Start.Origin.RunID == "" || req.Start.Origin.ToolID == "" {
		return Receipt{}, failure("run_context_required", "mail requires an active root Run and tool invocation")
	}
	d := digest(req.Start.Input)
	if old, err := s.Store.Invocation(req.Start.Origin.RunID, req.Start.Origin.ToolID); err == nil {
		if old.RequestDigest != d {
			return Receipt{}, failure("idempotency_conflict", "this invocation already used different arguments")
		}
		if old.Status == Accepting {
			if err = s.finishAcceptance(context.WithoutCancel(ctx), old); err != nil {
				return s.acceptanceFailure(old, err)
			}
		}
		current, _ := s.Store.Get(old.ID)
		s.Kick(current.ToChatID)
		return receipt(current), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Receipt{}, err
	}
	if req.Hop >= s.maxHops {
		return Receipt{}, failure("mail_hop_limit", "mail hop limit reached; return the result to the user")
	}
	prepared, err := s.Prepare(ctx, req)
	if err != nil {
		return Receipt{}, err
	}
	if gate, ok := s.port.(interface {
		BeginMailAcceptance(context.Context, Prepared) (context.Context, Prepared, func(), error)
	}); ok {
		var release func()
		ctx, prepared, release, err = gate.BeginMailAcceptance(ctx, prepared)
		if err != nil {
			return Receipt{}, err
		}
		defer release()
	}
	id := NewID("mail")
	files, err := s.prepareFiles(req.Start.Origin.ChatID, id, req.Files)
	if err != nil {
		return Receipt{}, err
	}
	input, err := json.Marshal(req.Start.Input)
	if err != nil {
		return Receipt{}, err
	}
	m := Mail{ID: id, Kind: Letter, Status: Accepting, CreatedAt: now(), UpdatedAt: now(), ToAgentKey: prepared.Request.AgentKey, ToChatID: prepared.ChatID, OpenedChat: prepared.OpenedChat,
		OriginType: "agent", FromAgentKey: req.Start.Origin.AgentKey, FromChatID: req.Start.Origin.ChatID, FromRunID: req.Start.Origin.RunID, FromToolID: req.Start.Origin.ToolID, Subject: req.Start.Origin.Subject,
		Input: input, Message: req.Start.Message, References: prepared.Request.References, Files: files, Hop: req.Hop + 1, RequestDigest: d}
	if prepared.Request.ModelKey != "" || prepared.Request.ReasoningEffort != "" {
		m.Model = &queryinput.QueryModelOptions{Key: prepared.Request.ModelKey, ReasoningEffort: prepared.Request.ReasoningEffort}
	}
	err = s.port.AcceptMail(ctx, req, prepared, func(g Grant) error {
		m.AccessLevel, m.AccessGrant = g.AccessLevel, g.AccessGrant
		stored, _, e := s.Store.Accept(m)
		if e != nil {
			return e
		}
		m = stored
		s.setScope(m.ID, g.Scope)
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	if err = s.finishAcceptance(context.WithoutCancel(ctx), m); err != nil {
		return s.acceptanceFailure(m, err)
	}
	current, err := s.Store.Get(m.ID)
	s.Kick(current.ToChatID)
	return receipt(current), err
}
func (s *Service) finishAcceptance(ctx context.Context, m Mail) error {
	if m.Status != Accepting {
		return nil
	}
	if err := s.port.EnsureMailChat(ctx, &m); err != nil {
		return err
	}
	if m.Kind == Reply && len(m.Files) == 0 && m.ReplyOfRun != "" {
		if err := s.replyFiles(&m); err != nil {
			return err
		}
		if _, err := s.Store.Change(m.ID, func(row *Mail) error { row.Files = m.Files; return nil }); err != nil {
			return err
		}
	}
	if err := s.copyFiles(m); err != nil {
		return err
	}
	_, err := s.change(m.ID, func(row *Mail) error {
		if row.Status != Accepting {
			return errStale
		}
		row.ToChatID, row.OpenedChat = m.ToChatID, m.OpenedChat
		row.Status = Queued
		row.ResumePhase = ""
		row.Error = nil
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

func (s *Service) acceptanceFailure(m Mail, cause error) (Receipt, error) {
	s.fail(m, cause)
	current, err := s.Store.Get(m.ID)
	if err == nil && current.Terminal() {
		return receipt(current), nil
	}
	return receipt(m), cause
}

// Kick never performs execution on the caller's stack (including terminal
// callbacks). A per-Chat lock serializes dispatch while inflight rows fence IO.
func (s *Service) Kick(chatID string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	lock := s.chatLocks[chatID]
	if lock == nil {
		lock = &chatLock{}
		s.chatLocks[chatID] = lock
	}
	lock.refs++
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		lock.mu.Lock()
		err := s.dispatch(chatID)
		lock.mu.Unlock()
		s.mu.Lock()
		if lock.refs--; lock.refs == 0 {
			delete(s.chatLocks, chatID)
		}
		s.mu.Unlock()
		if err != nil {
			log.Printf("[mailbox] dispatch chat=%s: %v", chatID, err)
		}
	}()
}
func (s *Service) dispatch(chatID string) error {
	rows, err := s.all(Filter{ChatID: chatID, Status: Queued, AllSubjects: true})
	if err != nil {
		return err
	}
	for _, m := range rows {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		scope, ok := s.scope(m.ID)
		if !ok {
			// A queued row was never handed to a Run in this attempt. Only a
			// person can supply a delivery identity again.
			_, err = s.change(m.ID, func(row *Mail) error {
				if row.Status != Queued {
					return errStale
				}
				row.ResumePhase = Queued
				row.Status = Suspended
				row.Evidence = "none"
				return nil
			})
			if err != nil && !errors.Is(err, errStale) {
				return err
			}
			continue
		}
		// Transient errors (I/O, shutdown) leave the queue in order; the next
		// trigger retries and a restart suspends it for a person.
		target, e := s.port.MailTarget(s.ctx, m)
		if e != nil {
			if !rejected(e) {
				return e
			}
			s.fail(m, e)
			continue
		}
		if target.Queue {
			return nil
		}
		if e = s.verifyFiles(m); e != nil {
			if !rejected(e) {
				return e
			}
			s.fail(m, e)
			continue
		}
		m, e = s.change(m.ID, func(row *Mail) error {
			if row.Status != Queued {
				return errStale
			}
			row.Status = Inflight
			row.Attempts++
			row.SteerID = row.ID + ":" + strconv.Itoa(row.Attempts)
			row.RunID = target.RunID
			row.Delivery = "inserted"
			if row.RunID == "" {
				row.RunID = chat.NewRunID()
				row.Delivery = "started"
			}
			return nil
		})
		if errors.Is(e, errStale) {
			continue
		}
		if e != nil {
			return e
		}
		result, e := s.port.DeliverMail(s.ctx, m, scope)
		if result.Rejected {
			s.fail(m, e)
			continue
		}
		if e != nil || !result.Accepted {
			_, changeErr := s.change(m.ID, func(row *Mail) error {
				if row.Attempts != m.Attempts || row.Status != Inflight {
					return errStale
				}
				if result.Unknown {
					row.ResumePhase = Inflight
					row.Status = Suspended
					row.Evidence = "unknown"
					row.Error = map[string]any{"code": "delivery_unknown", "message": fmt.Sprint(e)}
					if row.Kind == Notify || row.Kind == Ask {
						row.Status = Withdrawn
					}
				} else {
					row.Status = Queued
					row.RunID = ""
					row.SteerID = ""
					row.Delivery = ""
				}
				return nil
			})
			if changeErr != nil && !errors.Is(changeErr, errStale) {
				return changeErr
			}
			return e
		}
	}
	return nil
}
func (s *Service) Consumed(input *queryinput.MailInput, runID string) error {
	if input == nil {
		return nil
	}
	_, err := s.change(input.ID, func(row *Mail) error {
		if row.Attempts != input.Attempt || row.RunID != runID || row.Status != Inflight {
			return errStale
		}
		row.Status = Delivered
		row.ConsumedAt = now()
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

// Reconcile suspends unsettled mail at startup. A Chat whose evidence cannot
// be read is treated as ambiguous and never stops the Platform from starting.
func (s *Service) Reconcile(consumed func(Mail) (bool, error)) error {
	rows, err := s.Store.Unsettled()
	if err != nil {
		return err
	}
	for _, m := range rows {
		if m.Terminal() {
			continue
		}
		seen, e := consumed(m)
		if e != nil {
			log.Printf("[mailbox] reconcile evidence mail=%s chat=%s: %v", m.ID, m.ToChatID, e)
			seen = false
		}
		_, e = s.change(m.ID, func(row *Mail) error {
			if row.Kind == Notify || row.Kind == Ask {
				row.Status = Withdrawn
				return nil
			}
			if row.Kind == Reply && (seen || row.ConsumedAt != 0 || row.Status == Delivered) {
				row.Status = Delivered
				if row.ConsumedAt == 0 {
					row.ConsumedAt = now()
				}
				return nil
			}
			if row.Status != Suspended {
				row.ResumePhase = row.Status
			}
			row.Evidence = "unknown"
			if seen || row.ConsumedAt != 0 || row.ResumePhase == Delivered {
				row.Evidence = "consumed"
			} else if row.ResumePhase == Accepting || row.ResumePhase == Queued {
				row.Evidence = "none"
			}
			row.Status = Suspended
			return nil
		})
		if e != nil {
			log.Printf("[mailbox] reconcile mail=%s: %v", m.ID, e)
		}
	}
	return nil
}
func (s *Service) Action(ctx context.Context, id, action, subject string, scope controlscope.Scope) (Mail, error) {
	m, err := s.Store.Get(id)
	if err != nil {
		return m, err
	}
	if m.Subject != subject {
		return m, failure("mail_not_owned", "mail belongs to another principal")
	}
	if action != "redeliver" && action != "discard" {
		return m, failure("invalid_request", "action must be redeliver or discard")
	}
	if m.Status != Suspended || (m.Kind != Letter && m.Kind != Reply) || m.PurgedAt != 0 {
		return m, failure("mail_state_conflict", "only suspended mail can be redelivered or discarded")
	}
	if action == "redeliver" {
		// Installed before the row leaves suspended so no dispatch sees it
		// queued without an identity.
		s.setScope(id, scope)
	}
	m, err = s.change(id, func(row *Mail) error {
		if row.Status != Suspended {
			return failure("mail_state_conflict", "mail state changed")
		}
		if action == "discard" {
			row.Status = Discarded
			return nil
		}
		row.Status = Queued
		if row.ResumePhase == Accepting {
			row.Status = Accepting
		}
		row.RunID = ""
		row.SteerID = ""
		row.Delivery = ""
		return nil
	})
	if err != nil {
		return m, err
	}
	if action == "discard" {
		return m, nil
	}
	if m.Status == Accepting {
		if err = s.finishAcceptance(context.WithoutCancel(ctx), m); err != nil {
			s.fail(m, err)
			return m, err
		}
	}
	m, err = s.Store.Get(id)
	if err != nil {
		return m, err
	}
	s.Kick(m.ToChatID)
	return s.Store.Get(id)
}

// Header is generated at the model boundary and never persisted.
func (s *Service) Header(id string) string {
	m, err := s.Store.Get(id)
	if err != nil {
		return ""
	}
	name := strings.ToUpper(string(m.Kind[:1])) + string(m.Kind[1:])
	sender := "chat " + m.FromChatID
	if m.Kind == Ask {
		sender += ", run " + m.FromRunID
	}
	header := "[" + name + " " + m.ID + " from agent " + strconv.Quote(m.FromAgentKey) + " (" + sender + ")"
	// A purged row no longer names the correspondence it belonged to.
	related := m.PurgedAt == 0
	switch m.Kind {
	case Letter:
		header += ". Your final answer in this run will be sent back as the reply."
	case Reply:
		if ids, _ := s.Store.AnsweredLetters(m.ID); related && len(ids) > 0 {
			header += ", answering your letters " + strings.Join(ids, ", ")
		}
		header += ": " + m.Outcome
	case Notify:
		if related {
			header += ", about your letter " + m.ParentID
		}
		header += ". No reply needed."
	case Ask:
		if related {
			header += ", about your letter " + m.ParentID
		}
		header += ": " + m.AwaitingMode + " " + m.AwaitingID
	}
	header += "]\n"
	if len(m.Files) > 0 {
		header += "Attachments:"
		for _, f := range m.Files {
			header += " @chat/" + f.Path
		}
		header += "\n"
	}
	return header
}

func (s *Service) fail(m Mail, err error) {
	if m.Kind != Letter {
		_, _ = s.change(m.ID, func(row *Mail) error {
			if row.Terminal() {
				return errStale
			}
			row.Status = Withdrawn
			if row.Kind == Reply {
				row.ResumePhase = m.Status
				row.Status = Suspended
				row.Evidence = "none"
			}
			row.Error = errorData(err)
			return nil
		})
		return
	}
	r := s.newReply(m, "letter:"+m.ID, "", fmt.Sprint(err), "failed")
	m.Error = errorData(err)
	s.settle([]Mail{r}, []Mail{m}, true)
}

// settle commits replies and their letters. Each reply inherits its letter's
// delivery identity before commit so no dispatch can see it without one.
func (s *Service) settle(replies, letters []Mail, failed bool) error {
	for _, r := range replies {
		for _, l := range letters {
			if l.FromChatID == r.ToChatID && (!failed || r.ReplyKey == "letter:"+l.ID) {
				if scope, ok := s.scope(l.ID); ok {
					s.setScope(r.ID, scope)
				}
				break
			}
		}
	}
	inserted, settled, err := s.Store.Complete(replies, letters, failed)
	if err != nil {
		for _, r := range replies {
			s.dropScope(r.ID)
		}
		log.Printf("[mailbox] settle letters: %v", err)
		return err
	}
	stored := map[string]bool{}
	for _, r := range inserted {
		stored[r.ID] = true
	}
	for _, r := range replies {
		if !stored[r.ID] {
			s.dropScope(r.ID)
		}
	}
	for _, l := range settled {
		s.changed(l)
	}
	for _, r := range inserted {
		s.changed(r)
		s.Kick(r.ToChatID)
	}
	return nil
}
func (s *Service) newReply(m Mail, key, runID, text, outcome string) Mail {
	return Mail{ID: NewID("mail"), Kind: Reply, Status: Queued, CreatedAt: now(), UpdatedAt: now(), ToAgentKey: m.FromAgentKey, ToChatID: m.FromChatID,
		OriginType: "agent", FromAgentKey: m.ToAgentKey, FromChatID: m.ToChatID, FromRunID: runID, Subject: m.Subject, Message: text, Hop: m.Hop + 1, ReplyKey: key, ReplyOfRun: runID, Outcome: outcome, AccessLevel: "default"}
}

// outcome maps a Run finish reason to the reply outcome.
func outcome(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "cancel", "cancelled", "canceled", "interrupted":
		return "canceled"
	case "error", "failed", "fail", "timeout":
		return "failed"
	}
	return "completed"
}

// Completed runs once per finished Run; it reads only rows tied to that Run.
func (s *Service) Completed(completion chat.RunCompletion, hop int) error {
	s.mu.Lock()
	for key := range s.prepared {
		if strings.HasPrefix(key, completion.RunID+"\x00") {
			delete(s.prepared, key)
		}
	}
	s.mu.Unlock()
	asks, err := s.Store.AsksFromRun(completion.RunID)
	if err != nil {
		return err
	}
	for _, m := range asks {
		if m.Terminal() {
			continue
		}
		if _, err := s.change(m.ID, func(row *Mail) error {
			if row.Terminal() {
				return errStale
			}
			row.Status = Withdrawn
			return nil
		}); err != nil && !errors.Is(err, errStale) {
			return err
		}
	}
	rows, err := s.Store.ByRun(completion.RunID)
	if err != nil {
		return err
	}
	groups := map[string][]Mail{}
	for _, m := range rows {
		if m.Status == Inflight {
			if m.Delivery == "started" {
				// The Run opened for this mail ended before reading it; another
				// attempt would open the same failing Run again.
				s.fail(m, failure("mail_run_not_started", "the Run opened for this mail ended before reading it"))
				continue
			}
			_, err = s.change(m.ID, func(row *Mail) error {
				if row.Status != Inflight || row.Attempts != m.Attempts {
					return errStale
				}
				row.Status = Queued
				row.RunID = ""
				row.SteerID = ""
				row.Delivery = ""
				return nil
			})
			if err != nil && !errors.Is(err, errStale) {
				return err
			}
			continue
		}
		if m.Kind == Letter && m.Status == Delivered && m.AnsweredBy == "" {
			groups[m.FromChatID] = append(groups[m.FromChatID], m)
		}
	}
	chatIDs := make([]string, 0, len(groups))
	for chatID := range groups {
		chatIDs = append(chatIDs, chatID)
	}
	sort.Strings(chatIDs)
	var replies, letters []Mail
	for _, chatID := range chatIDs {
		group := groups[chatID]
		r := s.newReply(group[0], "run:"+completion.RunID+":"+chatID, completion.RunID, completion.AssistantText, outcome(completion.FinishReason))
		r.Hop = hop + 1
		attachmentErr := s.replyFiles(&r)
		if attachmentErr == nil {
			attachmentErr = s.copyFiles(r)
		}
		if attachmentErr != nil {
			// Preserve the complete result even if the filesystem is temporarily
			// unavailable. A human can resume attachment materialization later.
			r.Status = Suspended
			r.ResumePhase = Accepting
			r.Evidence = "none"
			r.Error = errorData(attachmentErr)
		}
		replies = append(replies, r)
		letters = append(letters, group...)
	}
	if len(replies) > 0 {
		return s.settle(replies, letters, false)
	}
	return nil
}

type artifactReader interface {
	PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
}

func (s *Service) artifacts() (artifactReader, bool) { p, ok := s.port.(artifactReader); return p, ok }
