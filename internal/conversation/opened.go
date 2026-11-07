package conversation

import (
	"errors"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

// ActiveRunsForChats is shared by tool and transport list projections.
func (s *Service) ActiveRunsForChats(ids []string) (map[string]contracts.RunStatusInfo, map[string]*contracts.ActiveRunConflictError, error) {
	if batch, ok := s.Runs.(interface {
		ActiveRunsForChats([]string) (map[string]contracts.RunStatusInfo, map[string]*contracts.ActiveRunConflictError)
	}); ok {
		runs, conflicts := batch.ActiveRunsForChats(ids)
		return runs, conflicts, nil
	}
	runs := map[string]contracts.RunStatusInfo{}
	conflicts := map[string]*contracts.ActiveRunConflictError{}
	for _, id := range ids {
		run, ok, err := s.ActiveRun(id)
		if err != nil {
			var conflict *contracts.ActiveRunConflictError
			if errors.As(err, &conflict) {
				conflicts[id] = conflict
				continue
			}
			return nil, nil, err
		}
		if ok {
			runs[id] = run
		}
	}
	return runs, conflicts, nil
}

type OpenedChat struct {
	ChatID    string     `json:"chatId"`
	AgentKey  string     `json:"agentKey"`
	ChatName  string     `json:"chatName"`
	MailID    string     `json:"mailId,omitempty"`
	RunID     string     `json:"runId,omitempty"`
	Status    string     `json:"status"`
	ActiveRun *OpenedRun `json:"activeRun,omitempty"`
}
type OpenedRun struct {
	RunID string                 `json:"runId"`
	State contracts.RunLoopState `json:"state"`
}

func (s *Service) ControlOpened(c ControlCaller) ([]OpenedChat, error) {
	evidence := map[string]map[string]any{}
	ids := []string{}
	var summaries []chat.Summary
	var err error
	store, hasPrimary := s.Chats.(chat.PrimaryStore)
	main := ""
	if hasPrimary {
		main, err = store.PrimaryChatID(c.AgentKey)
		if err != nil {
			return nil, err
		}
	}
	if main == c.ChatID && s.botAgent(c.AgentKey, "") {
		primary := false
		summaries, err = s.ListSummariesWithOptions(chat.ListOptions{AgentKey: c.AgentKey, Primary: &primary})
	} else {
		if s.MailboxOpened != nil {
			rows, e := s.MailboxOpened(c.AgentKey, c.Subject)
			if e != nil {
				return nil, e
			}
			for _, row := range rows {
				if id, ok := row["chatId"].(string); ok {
					evidence[id] = row
					ids = append(ids, id)
				}
			}
		}
		summaries, err = s.ListSummariesWithOptions(chat.ListOptions{ChatIDs: ids})
	}
	if err != nil {
		return nil, err
	}
	visible := summaries[:0]
	for _, summary := range summaries {
		if _, e := s.ResolveControlChat(c, summary.ChatID, false); e == nil {
			visible = append(visible, summary)
		}
	}
	summaries = visible

	ids = ids[:0]
	for _, summary := range summaries {
		ids = append(ids, summary.ChatID)
	}
	runs, conflicts, err := s.ActiveRunsForChats(ids)
	if err != nil {
		return nil, err
	}
	out := make([]OpenedChat, 0, len(summaries))
	for _, summary := range summaries {
		row := OpenedChat{ChatID: summary.ChatID, AgentKey: summary.AgentKey, ChatName: summary.ChatName, Status: "idle"}
		row.MailID, _ = evidence[summary.ChatID]["mailId"].(string)
		row.RunID, _ = evidence[summary.ChatID]["runId"].(string)
		if conflicts[summary.ChatID] != nil {
			return nil, conflicts[summary.ChatID]
		}
		if run, ok := runs[summary.ChatID]; ok {
			row.Status = "running"
			row.ActiveRun = &OpenedRun{RunID: run.RunID, State: run.State}
			if pending := summary.PendingAwaiting; pending != nil && pending.Mode != "wait" {
				row.Status = "awaiting"
			}
		}
		out = append(out, row)
	}
	return out, nil
}
