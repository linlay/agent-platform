package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/formhtml"
	"agent-platform/internal/stream"
)

func (s *Service) parent(runID string) (Mail, error) {
	rows, err := s.all(Filter{Kind: Letter, AllSubjects: true})
	if err != nil {
		return Mail{}, err
	}
	for _, m := range rows {
		if m.RunID == runID && m.Delivery == "started" && (m.Status == Delivered || m.Status == Inflight) {
			return m, nil
		}
	}
	return Mail{}, failure("mail_parent_required", "this Run was not opened by a letter")
}
func (s *Service) Notify(ctx context.Context, message string, files []string, origin contracts.RunOrigin, hop int) (Receipt, error) {
	if origin.RunID == "" || origin.ToolID == "" {
		return Receipt{}, failure("run_context_required", "notify requires a root Run and tool invocation")
	}
	input := map[string]any{"message": message, "files": files}
	d := digest(input)
	if old, err := s.Store.Invocation(origin.RunID, origin.ToolID); err == nil {
		if old.RequestDigest != d {
			return Receipt{}, failure("idempotency_conflict", "notify arguments changed")
		}
		return receipt(old), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if hop >= s.maxHops {
		return Receipt{}, failure("mail_hop_limit", "mail hop limit reached")
	}
	parent, err := s.parent(origin.RunID)
	if err != nil {
		return Receipt{}, err
	}
	scope, ok := s.scope(parent.ID)
	if !ok {
		return Receipt{}, failure("run_control_identity_unavailable", "mail delivery identity is unavailable")
	}
	m := s.newReply(parent, "", origin.RunID, message, "")
	m.Kind = Notify
	m.ParentID = parent.ID
	m.ReplyKey = ""
	m.ReplyOfRun = ""
	m.Hop = hop + 1
	m.FromToolID = origin.ToolID
	m.RequestDigest = d
	m.Input = json.RawMessage(jsonText(input))
	m.Files, err = s.prepareFiles(origin.ChatID, m.ID, files)
	if err != nil {
		return Receipt{}, err
	}
	if err = s.copyFiles(m); err != nil {
		return Receipt{}, err
	}
	s.setScope(m.ID, scope)
	m, _, err = s.Store.Accept(m)
	if err != nil {
		return Receipt{}, err
	}
	s.changed(m)
	s.Kick(m.ToChatID)
	return receipt(m), nil
}
func (s *Service) ObserveEvent(runID string, event stream.EventData) {
	switch event.Type {
	case "awaiting.ask":
		parent, err := s.parent(runID)
		if err != nil {
			return
		}
		hop := parent.Hop
		if p, ok := s.port.(interface{ MailRunHop(string) int }); ok {
			hop = p.MailRunHop(runID)
		}
		if hop >= s.maxHops {
			return
		}
		mode := event.String("mode")
		if mode != "question" && mode != "form" && mode != "approval" {
			return
		}
		source := "tool_approval"
		if mode == "question" {
			source = "ask_user_question"
		} else if mode == "form" {
			view, _ := event.Value("view").(map[string]any)
			if view["key"] == "ask_user_form" {
				source = "ask_user_form"
			}
		}
		snapshot := contracts.CloneMap(event.Payload)
		if source == "ask_user_form" {
			form, _ := snapshot["form"].(map[string]any)
			data, _ := form["data"].(map[string]any)
			fragment, _ := data["html"].(string)
			if fields, e := formhtml.Describe(fragment); e == nil {
				snapshot["fields"] = fields
			}
		}
		m := s.newReply(parent, "", runID, jsonText(snapshot), "")
		m.Kind = Ask
		m.ParentID = parent.ID
		m.Hop = hop + 1
		m.ReplyKey = ""
		m.ReplyOfRun = ""
		m.AwaitingID = event.String("awaitingId")
		m.AwaitingMode = mode
		m.AwaitingSource = source
		scope, ok := s.scope(parent.ID)
		if !ok {
			return
		}
		s.setScope(m.ID, scope)
		m, _, err = s.Store.Accept(m)
		if err != nil {
			return
		}
		s.changed(m)
		s.Kick(m.ToChatID)
	case "awaiting.answer":
		rows, err := s.all(Filter{Kind: Ask, AllSubjects: true})
		if err != nil {
			return
		}
		for _, m := range rows {
			if m.FromRunID == runID && m.AwaitingID == event.String("awaitingId") {
				updated, e := s.Store.Change(m.ID, func(row *Mail) error {
					if row.Status == Delivered {
						row.Status = Done
					} else if !row.Terminal() {
						row.Status = Withdrawn
					}
					return nil
				})
				if e == nil {
					s.changed(updated)
				}
			}
		}
	}
}
func (s *Service) Submit(ctx context.Context, req queryinput.SubmitRequest, exec *contracts.ExecutionContext) (queryinput.SubmitResponse, error) {
	if exec == nil {
		return queryinput.SubmitResponse{}, failure("run_context_required", "root Run required")
	}
	rows, err := s.all(Filter{ChatID: exec.Session.ChatID, Kind: Ask, Subject: exec.Session.Subject})
	if err != nil {
		return queryinput.SubmitResponse{}, err
	}
	for _, m := range rows {
		if m.FromRunID == req.RunID && m.AwaitingID == req.AwaitingID {
			if m.Status != Delivered {
				return queryinput.SubmitResponse{}, failure("already_resolved", "ask is no longer actionable")
			}
			return s.port.SubmitMail(ctx, m, req, exec)
		}
	}
	return queryinput.SubmitResponse{}, failure("mail_ask_not_owned", "no matching ask was delivered to this Chat")
}
func (s *Service) Owned(id, agentKey, subject string) (Mail, error) {
	m, err := s.Store.Get(id)
	if err != nil {
		return m, err
	}
	if m.Subject != subject || m.FromAgentKey != agentKey {
		return m, failure("mail_not_owned", "mail was not sent by this Agent and principal")
	}
	return m, nil
}
func (s *Service) OwnsRun(runID, agentKey, subject string) bool {
	rows, err := s.all(Filter{Subject: subject})
	if err != nil {
		return false
	}
	for _, m := range rows {
		if m.RunID == runID && m.FromAgentKey == agentKey && m.Delivery == "started" {
			return true
		}
	}
	return false
}
func (s *Service) OpenedChats(agentKey, subject string) ([]map[string]any, error) {
	rows, err := s.all(Filter{Subject: subject})
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, m := range rows {
		if m.FromAgentKey != agentKey || !m.OpenedChat || seen[m.ToChatID] || m.PurgedAt != 0 {
			continue
		}
		seen[m.ToChatID] = true
		out = append(out, map[string]any{"chatId": m.ToChatID, "agentKey": m.ToAgentKey, "mailId": m.ID, "runId": m.RunID, "status": m.Status})
	}
	if reader, ok := s.port.(interface {
		MailOpenedChats(string, string, []map[string]any) ([]map[string]any, error)
	}); ok {
		return reader.MailOpenedChats(agentKey, subject, out)
	}
	return out, nil
}
func (s *Service) PurgeChat(chatID string) error {
	rows, err := s.all(Filter{AllSubjects: true})
	if err != nil {
		return err
	}
	for _, m := range rows {
		if m.FromChatID != chatID && m.ToChatID != chatID {
			continue
		}
		if m.Kind == Letter && !m.Terminal() && m.ToChatID == chatID {
			s.fail(m, failure("chat_deleted", "receiving Chat was deleted"))
		}
		updated, e := s.Store.Change(m.ID, func(row *Mail) error {
			if !row.Terminal() {
				if row.Kind == Letter {
					row.Status = Failed
				} else {
					row.Status = Withdrawn
				}
			}
			row.Input = nil
			row.Message = ""
			row.References = nil
			row.Files = nil
			row.PurgedAt = now()
			return nil
		})
		if e != nil {
			return e
		}
		s.changed(updated)
	}
	return nil
}
func (s *Service) AskText(m Mail) string {
	if m.Kind != Ask {
		return m.Message
	}
	var snapshot map[string]any
	if json.Unmarshal([]byte(m.Message), &snapshot) != nil {
		return m.Message
	}
	return fmt.Sprintf("Waiting item (%s, source %s). Submit the matching param or params shape.\n%s", m.AwaitingMode, m.AwaitingSource, strings.TrimSpace(m.Message))
}
