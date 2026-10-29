package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/formhtml"
	"agent-platform/internal/stream"
)

func (s *Service) parent(runID string) (Mail, error) {
	m, err := s.Store.StartedLetter(runID)
	if errors.Is(err, ErrNotFound) {
		return Mail{}, failure("mail_parent_required", "this Run was not opened by a letter")
	}
	return m, err
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
	stored, inserted, err := s.Store.Accept(m)
	if !inserted {
		s.dropScope(m.ID)
	}
	if err != nil {
		return Receipt{}, err
	}
	s.changed(stored)
	s.Kick(stored.ToChatID)
	return receipt(stored), nil
}

// ObserveEvent runs for every event of every bound Run, so it reads only rows
// tied to the Run's own identity.
func (s *Service) ObserveEvent(runID string, event stream.EventData) {
	switch event.Type {
	case "awaiting.ask":
		mode := event.String("mode")
		if mode != "question" && mode != "form" && mode != "approval" {
			return
		}
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
		scope, ok := s.scope(parent.ID)
		if !ok {
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
		if form, _ := snapshot["form"].(map[string]any); form != nil {
			data, _ := form["data"].(map[string]any)
			if fragment, _ := data["html"].(string); fragment != "" {
				if fields, e := formhtml.Describe(fragment); e == nil {
					snapshot["fields"] = fields
				}
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
		s.setScope(m.ID, scope)
		stored, inserted, err := s.Store.Accept(m)
		if !inserted {
			s.dropScope(m.ID)
		}
		if err != nil || !inserted {
			return
		}
		s.changed(stored)
		s.Kick(stored.ToChatID)
	case "awaiting.answer":
		m, err := s.Store.Ask(runID, event.String("awaitingId"))
		if err != nil {
			return
		}
		_, _ = s.change(m.ID, func(row *Mail) error {
			switch {
			case row.Status == Delivered:
				row.Status = Done
			case !row.Terminal():
				row.Status = Withdrawn
			default:
				return errStale
			}
			return nil
		})
	}
}
func (s *Service) Submit(ctx context.Context, req queryinput.SubmitRequest, exec *contracts.ExecutionContext) (queryinput.SubmitResponse, error) {
	if exec == nil {
		return queryinput.SubmitResponse{}, failure("run_context_required", "root Run required")
	}
	m, err := s.Store.Ask(req.RunID, req.AwaitingID)
	if errors.Is(err, ErrNotFound) || err == nil && (m.ToChatID != exec.Session.ChatID || m.Subject != exec.Session.Subject) {
		return queryinput.SubmitResponse{}, failure("mail_ask_not_owned", "no matching ask was delivered to this Chat")
	}
	if err != nil {
		return queryinput.SubmitResponse{}, err
	}
	if m.Status != Delivered {
		return queryinput.SubmitResponse{}, failure("already_resolved", "ask is no longer actionable")
	}
	return s.port.SubmitMail(ctx, m, req, exec)
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
	owned, err := s.Store.OwnsRun(runID, agentKey, subject)
	return err == nil && owned
}
func (s *Service) OpenedChats(agentKey, subject string) ([]map[string]any, error) {
	rows, err := s.Store.OpenedChats(agentKey, subject)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, m := range rows {
		if seen[m.ToChatID] {
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
	rows, err := s.Store.ForChat(chatID)
	if err != nil {
		return err
	}
	for _, m := range rows {
		if m.Kind == Letter && !m.Terminal() && m.ToChatID == chatID {
			s.fail(m, failure("chat_deleted", "receiving Chat was deleted"))
		}
		if _, e := s.change(m.ID, func(row *Mail) error {
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
		}); e != nil {
			return e
		}
	}
	return nil
}

// Body is the text delivered to the receiving Chat. An ask keeps its awaiting
// snapshot in mail.db and is rendered here without form HTML.
func (s *Service) Body(m Mail) string {
	if m.Kind != Ask {
		return m.Message
	}
	var snapshot map[string]any
	if json.Unmarshal([]byte(m.Message), &snapshot) != nil {
		return m.Message
	}
	var b strings.Builder
	switch m.AwaitingMode {
	case "question":
		for i, raw := range items(snapshot["questions"]) {
			q := contracts.AnyMapNode(raw)
			b.WriteString(strconv.Itoa(i+1) + ". " + contracts.AnyStringNode(q["question"]))
			if labels := optionValues(q["options"], "label"); len(labels) > 0 {
				prompt := "select"
				if strings.EqualFold(contracts.AnyStringNode(q["type"]), "multi-select") {
					prompt = "select one or more"
				}
				b.WriteString("  " + prompt + ": " + strings.Join(labels, " | "))
			}
			b.WriteString("\n")
		}
	case "approval":
		decisions := []string{}
		for _, raw := range items(snapshot["approvals"]) {
			a := contracts.AnyMapNode(raw)
			command := contracts.AnyStringNode(a["command"])
			b.WriteString(contracts.AnyStringNode(a["toolName"]) + ": " + command)
			if d := contracts.AnyStringNode(a["description"]); d != "" && d != command {
				b.WriteString("  (" + d + ")")
			}
			b.WriteString("\n")
			for _, d := range optionValues(a["options"], "decision") {
				if !contains(decisions, d) {
					decisions = append(decisions, d)
				}
			}
		}
		if !contains(decisions, "reject") {
			decisions = append(decisions, "reject")
		}
		b.WriteString("Options: " + strings.Join(decisions, " | ") + ". Approval is accepted only if your own access level allows this operation.\n")
	case "form":
		form := contracts.AnyMapNode(snapshot["form"])
		if title := contracts.AnyStringNode(form["title"]); title != "" {
			b.WriteString(title + "\n")
		}
		var fields []formhtml.Field
		if raw, err := json.Marshal(snapshot["fields"]); err == nil {
			_ = json.Unmarshal(raw, &fields)
		}
		for _, f := range fields {
			line := "- " + f.Name + " (" + f.Type
			if f.Multiple {
				line += ", multiple"
			}
			line += ")"
			if len(f.Options) > 0 {
				line += ": " + strings.Join(f.Options, " | ")
			}
			b.WriteString(line + "\n")
		}
		if m.AwaitingSource == "tool_approval" {
			b.WriteString("Options: approve | reject. Approval is accepted only if your own access level allows this operation.\n")
		} else {
			b.WriteString("Options: approve with data | reject | dismiss.\n")
		}
	}
	b.WriteString(fmt.Sprintf("Answer with chat_submit runId=%q awaitingId=%q, or ask the user if unsure.", m.FromRunID, m.AwaitingID))
	return b.String()
}
func items(v any) []any {
	list, _ := v.([]any)
	return list
}
func optionValues(v any, key string) []string {
	out := []string{}
	for _, raw := range items(v) {
		if value := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(raw)[key])); value != "" {
			out = append(out, value)
		}
	}
	return out
}
func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
