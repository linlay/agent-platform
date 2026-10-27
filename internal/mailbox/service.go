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
	chatLocks     map[string]*sync.Mutex
	scopes        map[string]controlscope.Scope
	notifications contracts.NotificationSink
	wg            sync.WaitGroup
	closed        bool
	prepared      map[string]*preparedMail
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
	return &Service{Store: store, root: root, maxHops: maxHops, ctx: ctx, chatLocks: map[string]*sync.Mutex{}, scopes: map[string]controlscope.Scope{}, notifications: notifications}
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
func (s *Service) changed(m Mail) {
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
	updated, err := s.Store.Change(m.ID, func(row *Mail) error {
		if row.Status != Accepting {
			return errStale
		}
		row.ToChatID, row.OpenedChat = m.ToChatID, m.OpenedChat
		row.Status = Queued
		row.ResumePhase = ""
		row.Error = nil
		return nil
	})
	if err == nil {
		s.changed(updated)
	}
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
		lock = &sync.Mutex{}
		s.chatLocks[chatID] = lock
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		lock.Lock()
		defer lock.Unlock()
		if err := s.dispatch(chatID); err != nil {
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
			_, err = s.Store.Change(m.ID, func(row *Mail) error {
				row.ResumePhase = row.Status
				row.Status = Suspended
				row.Evidence = "unknown"
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}
		target, e := s.port.MailTarget(s.ctx, m)
		if e != nil {
			s.fail(m, e)
			continue
		}
		if target.Queue {
			return nil
		}
		if e = s.verifyFiles(m); e != nil {
			s.fail(m, e)
			continue
		}
		m, e = s.Store.Change(m.ID, func(row *Mail) error {
			if row.Status != Queued {
				return errStale
			}
			row.Status = Inflight
			row.Attempts++
			row.SteerID = row.ID + ":" + strconv.Itoa(row.Attempts)
			row.RunID = target.RunID
			row.Delivery = "inserted"
			if row.RunID == "" {
				row.RunID = NewID("r")
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
			_, changeErr := s.Store.Change(m.ID, func(row *Mail) error {
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
	m, err := s.Store.Change(input.ID, func(row *Mail) error {
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
	if err == nil {
		s.changed(m)
	}
	return err
}
func (s *Service) Reconcile(consumed func(Mail) (bool, error)) error {
	rows, err := s.all(Filter{AllSubjects: true})
	if err != nil {
		return err
	}
	for _, m := range rows {
		if m.Terminal() {
			continue
		}
		seen, e := consumed(m)
		if e != nil {
			return e
		}
		updated, e := s.Store.Change(m.ID, func(row *Mail) error {
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
			return e
		}
		s.changed(updated)
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
	if m.Status != Suspended || (m.Kind != Letter && m.Kind != Reply) || m.PurgedAt != 0 {
		return m, failure("mail_state_conflict", "only suspended mail can be redelivered or discarded")
	}
	if action != "redeliver" && action != "discard" {
		return m, failure("invalid_request", "action must be redeliver or discard")
	}
	m, err = s.Store.Change(id, func(row *Mail) error {
		if row.Status != Suspended {
			return failure("mail_state_conflict", "mail state changed")
		}
		if action == "discard" {
			row.Status = Discarded
			return nil
		}
		s.setScope(id, scope)
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
	s.changed(m)
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
func (s *Service) Header(id string) string {
	m, err := s.Store.Get(id)
	if err != nil {
		return ""
	}
	name := strings.ToUpper(string(m.Kind[:1])) + string(m.Kind[1:])
	header := "[" + name + " " + m.ID + " from agent " + strconv.Quote(m.FromAgentKey) + " (chat " + m.FromChatID + ")"
	switch m.Kind {
	case Letter:
		header += ". Your final answer in this run will be sent back as the reply."
	case Reply:
		rows, _ := s.all(Filter{AllSubjects: true})
		var ids []string
		for _, v := range rows {
			if v.AnsweredBy == m.ID {
				ids = append(ids, v.ID)
			}
		}
		sort.Strings(ids)
		header += ", answering your letters " + strings.Join(ids, ", ") + ": " + m.Outcome
	case Notify:
		header += ", about your letter " + m.ParentID + ". No reply needed."
	case Ask:
		header += ", about your letter " + m.ParentID + ": " + m.AwaitingMode + " " + m.AwaitingID + ". Answer with chat_submit runId=" + strconv.Quote(m.FromRunID) + " awaitingId=" + strconv.Quote(m.AwaitingID) + "."
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
		updated, changeErr := s.Store.Change(m.ID, func(row *Mail) error {
			if !row.Terminal() {
				row.Status = Withdrawn
				if row.Kind == Reply {
					row.ResumePhase = m.Status
					row.Status = Suspended
					row.Evidence = "none"
				}
				row.Error = errorData(err)
			}
			return nil
		})
		if changeErr == nil {
			s.changed(updated)
		}
		return
	}
	r := s.newReply(m, "letter:"+m.ID, "", fmt.Sprint(err), "failed")
	if scope, ok := s.scope(m.ID); ok {
		s.setScope(r.ID, scope)
	}
	m.Error = errorData(err)
	if e := s.Store.Complete([]Mail{r}, []Mail{m}, true); e != nil {
		log.Printf("[mailbox] fail %s: %v", m.ID, e)
		return
	}
	s.changed(r)
	s.Kick(r.ToChatID)
}
func (s *Service) newReply(m Mail, key, runID, text, outcome string) Mail {
	return Mail{ID: NewID("mail"), Kind: Reply, Status: Queued, CreatedAt: now(), UpdatedAt: now(), ToAgentKey: m.FromAgentKey, ToChatID: m.FromChatID,
		OriginType: "agent", FromAgentKey: m.ToAgentKey, FromChatID: m.ToChatID, FromRunID: runID, Subject: m.Subject, Message: text, Hop: m.Hop + 1, ReplyKey: key, ReplyOfRun: runID, Outcome: outcome, AccessLevel: "default"}
}
func (s *Service) Completed(completion chat.RunCompletion, hop int) error {
	s.mu.Lock()
	for key := range s.prepared {
		if strings.HasPrefix(key, completion.RunID+"\x00") {
			delete(s.prepared, key)
		}
	}
	s.mu.Unlock()
	rows, err := s.all(Filter{AllSubjects: true})
	if err != nil {
		return err
	}
	groups := map[string][]Mail{}
	for _, m := range rows {
		if m.Kind == Ask && m.FromRunID == completion.RunID && !m.Terminal() {
			updated, e := s.Store.Change(m.ID, func(row *Mail) error {
				if !row.Terminal() {
					row.Status = Withdrawn
				}
				return nil
			})
			if e != nil {
				return e
			}
			s.changed(updated)
		}
		if m.RunID != completion.RunID {
			continue
		}
		if m.Status == Inflight {
			_, err = s.Store.Change(m.ID, func(row *Mail) error {
				if row.Status == Inflight && row.Attempts == m.Attempts {
					row.Status = Queued
					row.RunID = ""
					row.SteerID = ""
					row.Delivery = ""
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if m.Kind == Letter && m.Status == Delivered && m.AnsweredBy == "" {
			groups[m.FromChatID] = append(groups[m.FromChatID], m)
		}
	}
	var replies, letters []Mail
	outcome := "completed"
	switch completion.FinishReason {
	case "error":
		outcome = "failed"
	case "cancel", "interrupted", "canceled":
		outcome = "canceled"
	}
	for chatID, group := range groups {
		r := s.newReply(group[0], "run:"+completion.RunID+":"+chatID, completion.RunID, completion.AssistantText, outcome)
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
		if scope, ok := s.scope(group[0].ID); ok {
			s.setScope(r.ID, scope)
		}
		replies = append(replies, r)
		letters = append(letters, group...)
	}
	if len(replies) > 0 {
		if err = s.Store.Complete(replies, letters, false); err != nil {
			return err
		}
		for _, r := range replies {
			s.changed(r)
			s.Kick(r.ToChatID)
		}
	}
	return nil
}

type artifactReader interface {
	PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
}

func (s *Service) artifacts() (artifactReader, bool) { p, ok := s.port.(artifactReader); return p, ok }
