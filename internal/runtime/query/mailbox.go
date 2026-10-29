package query

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func (s *Service) BindMailbox(box *mailbox.Service) {
	s.mailbox = box
	box.Bind(s)
	if runs, ok := s.deps.Runs.(interface{ SetMaintenanceIdle(func(string)) }); ok {
		runs.SetMaintenanceIdle(box.Kick)
	}
}
func (s *Service) MailRunHop(runID string) int {
	if p, ok := s.deps.Runs.(interface {
		Control(string) (*contracts.RunControl, bool)
	}); ok {
		if c, ok := p.Control(runID); ok {
			return c.MailHop()
		}
	}
	return 0
}

func (s *Service) MailOpenedChats(agentKey, subject string, items []map[string]any) ([]map[string]any, error) {
	seen := map[string]bool{}
	for _, item := range items {
		id, _ := item["chatId"].(string)
		seen[id] = true
	}
	summaries, err := s.deps.Chats.ListChats("", "")
	if err != nil {
		return nil, err
	}
	for _, summary := range summaries {
		if seen[summary.ChatID] || !strings.HasPrefix(summary.Source, "run-query:") || !s.mailChatOwned(summary, subject, map[string]bool{}) {
			continue
		}
		content, err := s.deps.Chats.LoadJSONLContent(summary.ChatID)
		if err != nil {
			return nil, err
		}
		scan := bufio.NewScanner(strings.NewReader(content))
		scan.Buffer(make([]byte, 65536), 64<<20)
		for scan.Scan() {
			var row map[string]any
			if json.Unmarshal(scan.Bytes(), &row) != nil || row["_type"] != "query" {
				continue
			}
			origin := queryinput.OriginFromPayload(row["origin"])
			if origin != nil && origin.Type == queryinput.OriginAgent && origin.AgentKey == agentKey {
				items = append(items, map[string]any{"chatId": summary.ChatID, "agentKey": summary.AgentKey})
				seen[summary.ChatID] = true
			}
			break
		}
		if err := scan.Err(); err != nil {
			return nil, err
		}
	}
	out := []map[string]any{}
	for _, item := range items {
		id, _ := item["chatId"].(string)
		summary, err := s.deps.Chats.Summary(id)
		if err != nil {
			return nil, err
		}
		if summary == nil {
			continue
		}
		active, found, err := s.deps.Runs.ActiveRunForChat(id)
		if err != nil {
			return nil, err
		}
		item["chatName"] = summary.ChatName
		item["active"] = found
		if found {
			item["activeRun"] = active
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Service) PrepareMail(ctx context.Context, req mailbox.Request) (mailbox.Prepared, error) {
	r, err := contracts.NormalizeRunStartRequest(req.Start)
	if err != nil {
		return mailbox.Prepared{}, err
	}
	p := mailbox.Prepared{Request: r, ChatID: r.ChatID, OpenedChat: req.NewChat}
	if p.ChatID == "" && !req.NewChat {
		store, ok := s.deps.Chats.(chat.PrimaryStore)
		if !ok {
			return p, runToolError("not_supported", "primary Chats are unavailable")
		}
		p.ChatID, err = store.PrimaryChatID(r.AgentKey)
		if err != nil {
			return p, err
		}
		p.Primary = p.ChatID == ""
		p.OpenedChat = p.Primary
	}
	if p.ChatID == "" {
		p.ChatID = newChatID()
	}
	if p.ChatID == r.Origin.ChatID {
		return p, runToolError("mail_self_address", "mail cannot address the current Chat; use newChat for independent work")
	}
	if !p.OpenedChat {
		summary, e := s.deps.Chats.Summary(p.ChatID)
		if e != nil {
			return p, e
		}
		if summary == nil {
			return p, runToolError("chat_not_found", "target Chat is missing or archived")
		}
		if summary.AgentKey != r.AgentKey || !s.mailChatOwned(*summary, r.Origin.Subject, map[string]bool{}) {
			return p, runToolError("mail_not_owned", "target Chat belongs to another Agent or principal")
		}
		allowed := false
		if store, ok := s.deps.Chats.(chat.PrimaryStore); ok {
			primary, e := store.PrimaryChatID(r.AgentKey)
			if e != nil {
				return p, e
			}
			allowed = primary == p.ChatID
		}
		for _, id := range req.UserChatReferences {
			if id == p.ChatID {
				allowed = true
			}
		}
		if s.mailbox != nil && !allowed {
			opened, e := s.mailbox.Store.OpenedBy(p.ChatID, r.Origin.AgentKey, r.Origin.Subject)
			if e != nil {
				return p, e
			}
			allowed = opened
		}
		if !allowed {
			content, e := s.deps.Chats.LoadJSONLContent(p.ChatID)
			if e != nil {
				return p, e
			}
			scanner := bufio.NewScanner(strings.NewReader(content))
			scanner.Buffer(make([]byte, 65536), 64<<20)
			for scanner.Scan() {
				var row map[string]any
				if json.Unmarshal(scanner.Bytes(), &row) != nil {
					continue
				}
				if row["_type"] == "query" {
					origin := queryinput.OriginFromPayload(row["origin"])
					if origin != nil && origin.AgentKey == r.Origin.AgentKey {
						allowed = true
					}
					break
				}
			}
			if e = scanner.Err(); e != nil {
				return p, e
			}
		}
		if !allowed {
			return p, runToolError("mail_address_forbidden", "Chat must be created by this Agent, referenced by the user, or the target Agent's primary Chat")
		}
	}
	p.Request.ChatID = p.ChatID
	review := p.Request
	if p.OpenedChat {
		review.ChatID = ""
	}
	p.Plan, err = s.PrepareRunStart(ctx, review)
	if err == nil {
		p.Plan.RequestDigest = contracts.RunStartRequestDigest(p.Request)
		p.Plan.ApprovalDigest = contracts.RunStartApprovalDigest(p.Plan.RequestDigest, p.Plan.ParentAccessLevel, p.Plan.ParentAccessVersion, p.Plan.AccessLevel)
	}
	return p, err
}
func (s *Service) mailChatOwned(summary chat.Summary, subject string, seen map[string]bool) bool {
	if seen[summary.ChatID] || len(seen) > 16 {
		return false
	}
	seen[summary.ChatID] = true
	// Mailbox creates Chats with a query:<subject> source, so ownership never
	// depends on mail rows, which may be written before ownership is checked.
	if owner, ok := strings.CutPrefix(summary.Source, "query:"); ok {
		return owner == subject
	}
	if strings.HasPrefix(summary.Source, "run-query:") {
		content, err := s.deps.Chats.LoadJSONLContent(summary.ChatID)
		if err != nil {
			return false
		}
		scan := bufio.NewScanner(strings.NewReader(content))
		scan.Buffer(make([]byte, 65536), 64<<20)
		for scan.Scan() {
			var row map[string]any
			if json.Unmarshal(scan.Bytes(), &row) != nil || row["_type"] != "query" {
				continue
			}
			origin := queryinput.OriginFromPayload(row["origin"])
			if origin == nil || origin.ChatID == "" {
				return false
			}
			parent, err := s.deps.Chats.Summary(origin.ChatID)
			return err == nil && parent != nil && s.mailChatOwned(*parent, subject, seen)
		}
	}
	return subject == "" && !strings.HasPrefix(summary.Source, "query:")
}
func (s *Service) AcceptMail(ctx context.Context, req mailbox.Request, p mailbox.Prepared, persist func(mailbox.Grant) error) error {
	r := p.Request
	r.Review = req.Start.Review
	acceptor, ok := s.deps.Runs.(runAccessLevelAcceptor)
	if !ok {
		return runToolError("run_start_authorization_unavailable", "atomic mail authorization unavailable")
	}
	scope, err := s.runControlScopes().Load(r.Origin.RunID)
	if err != nil {
		return err
	}
	active, err := acceptor.AcceptAtRunAccessLevel(r.Origin.RunID, func(parent string, version int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		level, escalates := contracts.ResolveRunStartAccessLevel(r.AccessLevel, parent)
		source := "inherited"
		if r.AccessLevel != "" {
			source = "explicit"
		}
		grant := map[string]any{"parentAccessLevel": parent, "parentAccessVersion": version, "accessLevel": level, "source": source, "requestDigest": p.Plan.RequestDigest}
		if review := r.Review; review != nil {
			d := contracts.RunStartApprovalDigest(p.Plan.RequestDigest, parent, version, level)
			if parent != review.ParentAccessLevel || version != review.ParentAccessVersion || d != review.ApprovalDigest {
				return runToolError("run_start_review_stale", "caller permission changed after review; issue a new chat_send invocation")
			}
			if review.Consume == nil || !review.Consume(d) {
				return runToolError("run_start_approval_required", "the one-shot approval is missing or already used")
			}
			grant["source"], grant["approvalDigest"] = "approved", d
		} else if escalates {
			return runToolError("run_start_approval_required", "mail permission exceeds the caller Run; human approval is required")
		}
		if level != p.Plan.AccessLevel {
			return runToolError("run_parent_access_level_changed", "caller permission changed during mail admission")
		}
		return persist(mailbox.Grant{AccessLevel: level, AccessGrant: grant, Scope: scope})
	})
	if err != nil {
		return err
	}
	if !active {
		return runToolError("run_parent_not_active", "calling Run is no longer active")
	}
	return nil
}

type mailMutationHeld struct{}

// Only missing-primary reservations need the shared mutation lock. Reviews are
// prepared before this boundary; no lock is held while waiting for a person.
func (s *Service) BeginMailAcceptance(ctx context.Context, p mailbox.Prepared) (context.Context, mailbox.Prepared, func(), error) {
	release := func() {}
	if !p.Primary {
		return ctx, p, release, nil
	}
	if locker, ok := s.deps.PrimaryChats.(interface{ LockMutation() func() }); ok {
		release = locker.LockMutation()
	}
	store, ok := s.deps.Chats.(chat.PrimaryStore)
	if !ok {
		release()
		return ctx, p, func() {}, runToolError("not_supported", "primary Chats unavailable")
	}
	current, err := store.PrimaryChatID(p.Request.AgentKey)
	if err != nil {
		release()
		return ctx, p, func() {}, err
	}
	if current != "" && current != p.ChatID {
		if current == p.Request.Origin.ChatID {
			release()
			return ctx, p, func() {}, runToolError("mail_self_address", "primary Chat is the sending Chat")
		}
		if err := s.mailPrimaryOwned(current, p.Request.AgentKey, p.Request.Origin.Subject); err != nil {
			release()
			return ctx, p, func() {}, err
		}
		p.ChatID = current
		p.OpenedChat = false
		p.Request.ChatID = current
		p.Plan.RequestDigest = contracts.RunStartRequestDigest(p.Request)
		p.Plan.ApprovalDigest = contracts.RunStartApprovalDigest(p.Plan.RequestDigest, p.Plan.ParentAccessLevel, p.Plan.ParentAccessVersion, p.Plan.AccessLevel)
	}
	return context.WithValue(ctx, mailMutationHeld{}, true), p, release, nil
}

func (s *Service) EnsureMailChat(ctx context.Context, m *mailbox.Mail) error {
	if !m.OpenedChat {
		return nil
	}
	if locker, ok := s.deps.PrimaryChats.(interface{ LockMutation() func() }); ok && ctx.Value(mailMutationHeld{}) != true {
		release := locker.LockMutation()
		defer release()
	}
	var input map[string]any
	_ = json.Unmarshal(m.Input, &input)
	primary := input["chatId"] == nil && input["newChat"] != true && input["newChat"] != "true"
	if primary {
		store, ok := s.deps.Chats.(chat.PrimaryStore)
		if !ok {
			return runToolError("not_supported", "primary Chats unavailable")
		}
		current, err := store.PrimaryChatID(m.ToAgentKey)
		if err != nil {
			return err
		}
		if current != "" && current != m.ToChatID {
			if current == m.FromChatID {
				return runToolError("mail_self_address", "primary Chat is the sending Chat")
			}
			if err := s.mailPrimaryOwned(current, m.ToAgentKey, m.Subject); err != nil {
				return err
			}
			m.ToChatID = current
			m.OpenedChat = false
			return nil
		}
	}
	def, ok := s.deps.Registry.AgentDefinition(m.ToAgentKey)
	if !ok {
		return runToolError("agent_not_found", "target Agent was removed")
	}
	source := "query:" + m.Subject
	name, _ := input["chatName"].(string)
	summary, created, err := s.deps.Chats.EnsureChatWithInitialName(m.ToChatID, m.ToAgentKey, m.Message, source, def.Mode, name)
	if err != nil {
		return err
	}
	if summary.AgentKey != m.ToAgentKey || !s.mailChatOwned(summary, m.Subject, map[string]bool{}) {
		return runToolError("mail_not_owned", "reserved Chat identity is occupied by another owner")
	}
	if primary && s.deps.PrimaryChats != nil {
		// Only fills a missing pointer. A primary:true query does not take the
		// mutation lock, so a pointer it set meanwhile is kept; the letter then
		// stays in the Chat it opened.
		if backfill, ok := s.deps.PrimaryChats.(interface{ BackfillPrimaryChat(string, string) error }); ok {
			err = backfill.BackfillPrimaryChat(m.ToAgentKey, m.ToChatID)
		} else {
			err = s.deps.PrimaryChats.PublishPrimaryChat(m.ToAgentKey, m.ToChatID)
		}
		if err != nil {
			return err
		}
	}
	if created {
		s.broadcast("chat.created", chatCreatedPayload(summary.ChatID, summary.ChatName, summary.AgentKey, summary.CreatedAt, summary.Source))
	}
	return ctx.Err()
}

// mailPrimaryOwned checks a primary pointer read after preparation, since the
// pointer is per Agent rather than per principal.
func (s *Service) mailPrimaryOwned(chatID, agentKey, subject string) error {
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return err
	}
	if summary == nil {
		return runToolError("chat_not_found", "target Chat is missing or archived")
	}
	if summary.AgentKey != agentKey || !s.mailChatOwned(*summary, subject, map[string]bool{}) {
		return runToolError("mail_not_owned", "target Chat belongs to another Agent or principal")
	}
	return nil
}

// mailDefinitive separates admission and validation verdicts from I/O or
// shutdown failures that a later attempt may not repeat.
func mailDefinitive(err error) bool {
	var tool *contracts.RunToolError
	if errors.As(err, &tool) {
		return tool.Code != "internal_error"
	}
	var request *runtimetypes.RequestError
	if errors.As(err, &request) {
		return request.Status < 500
	}
	return errors.Is(err, chat.ErrChatNotFound)
}
func mailRejectDefinitive(err error) error {
	if err != nil && mailDefinitive(err) {
		return mailbox.Reject(err)
	}
	return err
}

// MailTarget marks delivery-time check failures with mailbox.Reject; any other
// error leaves the mail queued.
func (s *Service) MailTarget(ctx context.Context, m mailbox.Mail) (mailbox.Target, error) {
	if m.ToChatID == m.FromChatID {
		return mailbox.Target{}, mailbox.Reject(runToolError("mail_self_address", "mail cannot address the sending Chat"))
	}
	def, team, release, ok := catalogview.AcquireRun(s.deps.Registry, m.ToAgentKey)
	if release != nil {
		defer release()
	}
	if !ok {
		return mailbox.Target{}, mailbox.Reject(runToolError("agent_not_found", "target Agent was removed"))
	}
	if team != nil {
		if err := catalogview.ValidateTeamSnapshot(*team); err != nil {
			return mailbox.Target{}, mailbox.Reject(err)
		}
	}
	if _, err := s.deps.Sessions.PrepareRunReferences(ctx, contracts.QuerySession{ChatID: m.ToChatID, AgentKey: m.ToAgentKey, Subject: m.Subject}, def, m.References, false, false, true); err != nil {
		if ctx.Err() != nil {
			return mailbox.Target{}, err
		}
		return mailbox.Target{}, mailRejectDefinitive(err)
	}
	summary, err := s.deps.Chats.Summary(m.ToChatID)
	if err != nil {
		return mailbox.Target{}, err
	}
	if summary == nil {
		return mailbox.Target{}, mailbox.Reject(runToolError("chat_not_found", "target Chat is missing or archived"))
	}
	if summary.AgentKey != m.ToAgentKey || !s.mailChatOwned(*summary, m.Subject, map[string]bool{}) {
		return mailbox.Target{}, mailbox.Reject(runToolError("mail_not_owned", "target owner changed"))
	}
	if m.Model != nil {
		copy := *m.Model
		if err = s.ValidateQueryModelOptions(&copy, def); err != nil {
			return mailbox.Target{}, mailbox.Reject(err)
		}
	}
	if runs, ok := s.deps.Runs.(interface{ ChatMaintenanceActive(string) bool }); ok && runs.ChatMaintenanceActive(m.ToChatID) {
		return mailbox.Target{Queue: true}, nil
	}
	active, ok, err := s.deps.Runs.ActiveRunForChat(m.ToChatID)
	if err != nil {
		return mailbox.Target{}, err
	}
	if ok {
		return mailbox.Target{RunID: active.RunID, Queue: active.PlanningMode || active.State == contracts.RunLoopStateCompacting}, nil
	}
	if gate := s.AwaitingQueryGateError(m.ToChatID, summary); gate != nil {
		return mailbox.Target{Queue: true}, nil
	}
	return mailbox.Target{}, ctx.Err()
}
func (s *Service) DeliverMail(ctx context.Context, m mailbox.Mail, scope controlscope.Scope) (mailbox.DeliveryResult, error) {
	refs := append([]queryinput.Reference(nil), m.References...)
	for _, f := range m.Files {
		size := f.Size
		refs = append(refs, queryinput.Reference{Type: "file", Name: f.Name, URL: "@chat/" + f.Path, SizeBytes: &size})
	}
	origin := &queryinput.Origin{Type: queryinput.OriginAgent, AgentKey: m.FromAgentKey, ChatID: m.FromChatID, RunID: m.FromRunID, ToolID: m.FromToolID}
	if m.Delivery == "inserted" {
		body := s.mailbox.Body(m)
		req := queryinput.SteerRequest{ChatID: m.ToChatID, RunID: m.RunID, SteerID: m.SteerID, AgentKey: m.ToAgentKey, Message: body, References: refs, Origin: origin, Mail: m.Metadata()}
		req.ModelMessage = s.mailbox.Header(m.ID) + body
		if response, statusErr, ok := s.deps.Proxy.Steer(req); ok {
			if statusErr != nil {
				return mailbox.DeliveryResult{Unknown: statusErr.OutcomeUnknown, Rejected: !statusErr.OutcomeUnknown}, statusErr
			}
			return mailbox.DeliveryResult{Accepted: response.Accepted, RunID: m.RunID}, nil
		}
		ack := s.deps.Runs.Steer(req)
		if !ack.Accepted && ack.Status == "invalid_reference" {
			return mailbox.DeliveryResult{Rejected: true}, runToolError("reference_unavailable", ack.Detail)
		}
		return mailbox.DeliveryResult{Accepted: ack.Accepted, RunID: m.RunID}, nil
	}
	body := s.mailbox.Body(m)
	input, _ := json.Marshal(map[string]any{"message": body, "references": refs})
	cmd := runtimetypes.QueryCommand{RunID: m.RunID, ChatID: m.ToChatID, AgentKey: m.ToAgentKey, Message: body, References: refs, Origin: origin, Mail: m.Metadata(), AccessLevel: m.AccessLevel, AccessGrant: m.AccessGrant, Model: m.Model, OriginalQuery: input, SkipInteractionPolicy: true, Identity: &contracts.AuthIdentity{Subject: m.Subject}}
	cmd.ModelMessage = s.mailbox.Header(m.ID) + body
	ctx = controlscope.WithContext(s.backgroundCtx, scope)
	_, err := s.StartQuery(ctx, cmd)
	if err != nil {
		var request *runtimetypes.RequestError
		if errors.As(err, &request) && (request.Code == activeRunConflictCode || request.Code == awaitingPendingCode || request.Code == "compact_in_progress") {
			return mailbox.DeliveryResult{}, nil
		}
		// Once registered, a Run may already be working on this mail.
		if _, registered := s.deps.Runs.RunStatus(m.RunID); registered {
			return mailbox.DeliveryResult{Unknown: true}, err
		}
		if s.backgroundCtx.Err() == nil && mailDefinitive(err) {
			return mailbox.DeliveryResult{Rejected: true}, err
		}
		return mailbox.DeliveryResult{}, err
	}
	return mailbox.DeliveryResult{Accepted: true, RunID: m.RunID}, nil
}
func (s *Service) PublishedArtifacts(chatID string) ([]chat.ArtifactManifestItem, error) {
	if reader, ok := s.deps.Chats.(interface {
		PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
	}); ok {
		return reader.PublishedArtifacts(chatID)
	}
	return nil, nil
}
func (s *Service) MailConsumedEvidence(m mailbox.Mail) (bool, error) {
	content, err := s.deps.Chats.LoadJSONLContent(m.ToChatID)
	if err != nil {
		if errors.Is(err, chat.ErrChatNotFound) {
			return false, nil
		}
		return false, err
	}
	scan := bufio.NewScanner(strings.NewReader(content))
	scan.Buffer(make([]byte, 65536), 64<<20)
	for scan.Scan() {
		var row map[string]any
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			continue
		}
		if row["_type"] == "query" && row["mailId"] == m.ID {
			return true, nil
		}
		if row["_type"] == "steer" {
			steer, _ := row["steer"].(map[string]any)
			if steer["mailId"] == m.ID {
				return true, nil
			}
		}
	}
	return false, scan.Err()
}
func (s *Service) bindMailboxSession(req runtimetypes.QueryCommand, session *contracts.QuerySession) {
	if s.mailbox == nil || req.Lane != "" {
		return
	}
	session.Mail = req.Mail
	if req.Mail == nil && (req.Origin == nil || req.Origin.Type == queryinput.OriginUser) {
		session.UserChatReferences = queryinput.ResourceIDs(req.References, "chat")
	}
	session.MailHeaders = s.mailbox.Header
	// Only a letter derives the Run from its sender. A reply, notification or
	// ask wakes the receiving Chat's own Agent, which keeps its Chat tools and
	// gives the sender no control over the Run.
	if req.Mail != nil && req.Mail.Kind == string(mailbox.Letter) {
		session.RunOrigin = &contracts.RunOrigin{AgentKey: req.Origin.AgentKey, ChatID: req.Origin.ChatID, RunID: req.Origin.RunID, ToolID: req.Origin.ToolID, Subject: session.Subject}
	}
	session.RuntimeEvent = func(event stream.EventData) {
		if event.Type == "request.query" || event.Type == "request.steer" {
			if input := queryinput.MailFromPayload(event.Payload); input != nil {
				if err := s.mailbox.Consumed(input, req.RunID); err != nil {
					log.Printf("[mailbox] consume: %v", err)
				}
			}
		}
		s.mailbox.ObserveEvent(req.RunID, event)
	}
	// Mail is answered from the in-memory completion even when the Chat summary
	// could not be written; otherwise the sender would wait until a restart.
	session.RuntimeComplete = func(runID, reason, text string, persisted bool) {
		if !persisted {
			log.Printf("[mailbox] run=%s completion was not persisted; answering mail from the in-memory result", runID)
		}
		hop := 0
		if ctl, ok := s.deps.Runs.(interface {
			Control(string) (*contracts.RunControl, bool)
		}); ok {
			if c, ok := ctl.Control(runID); ok {
				hop = c.MailHop()
			}
		}
		if err := s.mailbox.Completed(chat.RunCompletion{RunID: runID, ChatID: req.ChatID, AgentKey: req.AgentKey, FinishReason: reason, AssistantText: text}, hop); err != nil {
			log.Printf("[mailbox] completion: %v", err)
		}
	}
	session.RuntimeIdle = func() { s.mailbox.Kick(req.ChatID) }
}
func (s *Service) SubmitMail(ctx context.Context, m mailbox.Mail, req queryinput.SubmitRequest, exec *contracts.ExecutionContext) (queryinput.SubmitResponse, error) {
	if exec == nil || exec.RunControl == nil {
		return queryinput.SubmitResponse{}, runToolError("run_context_required", "active Run required")
	}
	awaiting, ok := s.deps.Runs.LookupAwaiting(req.RunID, req.AwaitingID)
	if !ok {
		return queryinput.SubmitResponse{}, runToolError("already_resolved", "waiting item is no longer active")
	}
	if err := ValidateSubmitParams(awaiting, req); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	approve := false
	if m.AwaitingSource == "tool_approval" {
		if req.Param != nil {
			approve = req.Param["decision"] == "approve"
		} else {
			items, _ := queryinput.DecodeSubmitParams(req.Params)
			for _, item := range items {
				if item["decision"] == "approve" || item["decision"] == "approve_rule_run" {
					approve = true
				}
			}
		}
	}
	req.ChatID = m.FromChatID
	req.AgentKey = m.FromAgentKey
	req.SubmitID = "mail-submit:" + exec.Session.RunID + ":" + exec.CurrentToolID
	req.Origin = &queryinput.Origin{Type: queryinput.OriginAgent, AgentKey: exec.Session.AgentKey, ChatID: exec.Session.ChatID, RunID: exec.Session.RunID, ToolID: exec.CurrentToolID}
	hop := exec.RunControl.MailHop() + 1
	req.MailHop = &hop
	var response queryinput.SubmitResponse
	err := exec.RunControl.AcceptAtAccessLevel(func(level string, version int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if approve {
			if awaiting.DelegateApproval == nil {
				return runToolError("approval_requires_human", "this approval requires a human")
			}
			if err := awaiting.DelegateApproval(ctx, level, req); err != nil {
				return err
			}
		}
		req.AccessGrant = map[string]any{"source": "mailbox", "accessLevel": level, "accessVersion": version}
		var err error
		response, _, _, err = s.resolveSubmit(req)
		return err
	})
	return response, err
}

var _ mailbox.Port = (*Service)(nil)
