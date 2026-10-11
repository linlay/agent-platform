package runstate

import (
	"sort"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

// Snapshot maps process-local run state to the stable runtime result used by
// in-process callers. Transport adapters decide how to encode that result.
func Snapshot(runs contracts.RunManager, chats chat.Store, runID string) (contracts.RunSnapshot, error) {
	runID = strings.TrimSpace(runID)
	status, ok := runs.RunStatus(runID)
	if !ok {
		if reader, valid := runs.(interface {
			StoredRunSnapshot(string) (contracts.RunSnapshot, error)
		}); valid {
			if snapshot, err := reader.StoredRunSnapshot(runID); err == nil {
				if chats != nil {
					if detail, err := chats.LoadChat(snapshot.ChatID); err == nil {
						events := []stream.EventData{}
						for _, event := range detail.Events {
							if event.String("runId") == runID {
								events = append(events, event)
							}
						}
						ApplyEventSnapshot(&snapshot, events)
					}
				}
				return snapshot, nil
			}
		}
		return contracts.RunSnapshot{}, &contracts.RunToolError{Code: "run_not_found", Message: "run not found"}
	}
	snapshot := contracts.RunSnapshot{
		RunID:       status.RunID,
		AccessLevel: status.AccessLevel,
		ChatID:      status.ChatID,
		AgentKey:    status.AgentKey,

		Status:      PublicStatus(status.State),
		LastSeq:     status.LastSeq,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Origin:      CloneRunOrigin(status.RunOrigin),
	}
	if status.State == contracts.RunLoopStateWaitingSubmit {
		if lister, ok := runs.(contracts.ActiveAwaitingLister); ok {
			waiters := lister.ActiveAwaitings(runID)
			sort.Slice(waiters, func(i, j int) bool { return publicAwaitingID(waiters[i]) < publicAwaitingID(waiters[j]) })
			snapshot.AwaitingCount = len(waiters)
			for _, awaiting := range waiters {
				publicID := strings.TrimSpace(awaiting.PublicAwaitingID)
				if publicID == "" {
					publicID = strings.TrimSpace(awaiting.AwaitingID)
				}
				snapshot.Awaiting = &contracts.RunAwaiting{
					AwaitingID: publicID,
					Mode:       awaiting.Mode,
					ItemCount:  awaiting.ItemCount,
					Summaries:  append([]contracts.ApprovalSummary(nil), awaiting.Summaries...),
					Truncated:  awaiting.SummariesTruncated,
				}
				if awaiting.Mode == "question" {
					snapshot.Awaiting.Questions = append([]any(nil), awaiting.Questions...)
				}
				break
			}
		}
	}
	// Recovered waiters live in the deferred registry rather than RunControl.
	// Read their persisted ask only when no active in-memory waiter is present.
	if status.State == contracts.RunLoopStateWaitingSubmit && snapshot.Awaiting == nil && chats != nil {
		if summary, err := chats.Summary(snapshot.ChatID); err == nil && summary != nil && summary.PendingAwaiting != nil && summary.PendingAwaiting.RunID == runID {
			pending := summary.PendingAwaiting
			if ask, err := chats.LoadAwaitingAsk(snapshot.ChatID, pending.AwaitingID); err == nil && ask != nil {
				payload := ask.Payload
				mode := ask.Mode
				if mode == "" {
					mode = pending.Mode
				}
				awaiting := &contracts.RunAwaiting{AwaitingID: pending.AwaitingID, Mode: mode}
				key := map[string]string{"question": "questions", "approval": "approvals"}[mode]
				if items, ok := payload[key].([]any); ok {
					awaiting.ItemCount = len(items)
				}
				if (mode == "planning" || mode == "form") && payload[mode] != nil {
					awaiting.ItemCount = 1
				}
				if mode == "question" {
					awaiting.Questions, _ = payload["questions"].([]any)
				}
				awaiting.Summaries, awaiting.Truncated = contracts.SummarizeApprovals(payload["approvals"])
				snapshot.Awaiting = awaiting
				snapshot.AwaitingCount = 1
			}
		}
	}
	if eventBus, exists := runs.EventBus(runID); exists {
		ApplyEventSnapshot(&snapshot, eventBus.Snapshot())
	}
	if snapshot.Status == "completed" && chats != nil {
		if summary, err := chats.Summary(snapshot.ChatID); err == nil && summary != nil && strings.TrimSpace(summary.LastRunID) == runID {
			snapshot.Content = summary.LastRunContent
		}
	}
	return snapshot, nil
}

func PublicStatus(state contracts.RunLoopState) string {
	switch state {
	case contracts.RunLoopStateWaitingSubmit:
		return "awaiting"
	case contracts.RunLoopStateCompleted:
		return "completed"
	case contracts.RunLoopStateFailed:
		return "failed"
	case contracts.RunLoopStateCancelled:
		return "interrupted"
	default:
		return "running"
	}
}

func ApplyEventSnapshot(snapshot *contracts.RunSnapshot, events []stream.EventData) {
	if snapshot == nil {
		return
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		switch event.Type {
		case "content.snapshot":
			if snapshot.Content == "" && strings.TrimSpace(event.String("taskId")) == "" {
				snapshot.Content = event.String("text")
			}
		case "awaiting.ask":
			if snapshot.Awaiting != nil && snapshot.Awaiting.Mode == "question" && snapshot.Awaiting.Payload == nil && event.String("awaitingId") == snapshot.Awaiting.AwaitingID {
				snapshot.Awaiting.Payload = contracts.CloneMap(event.Payload)
			}
		case "run.error":
			if snapshot.Error == nil {
				if payload, ok := event.Payload["error"].(map[string]any); ok {
					snapshot.Error = contracts.CloneMap(payload)
				} else {
					snapshot.Error = contracts.CloneMap(event.Payload)
				}
			}
		case "run.cancel":
			if snapshot.Error == nil {
				snapshot.Error = contracts.CloneMap(event.Payload)
			}
		}
	}
}

func CloneRunOrigin(origin *contracts.RunOrigin) *contracts.RunOrigin {
	if origin == nil {
		return nil
	}
	cloned := *origin
	return &cloned
}

func publicAwaitingID(item contracts.AwaitingSubmitContext) string {
	if id := strings.TrimSpace(item.PublicAwaitingID); id != "" {
		return id
	}
	return item.AwaitingID
}
