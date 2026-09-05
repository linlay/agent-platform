package runstate

import (
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
		return contracts.RunSnapshot{}, &contracts.RunToolError{Code: "run_not_found", Message: "run not found"}
	}
	snapshot := contracts.RunSnapshot{
		RunID:       status.RunID,
		ChatID:      status.ChatID,
		AgentKey:    status.AgentKey,
		TeamID:      status.TeamID,
		Status:      PublicStatus(status.State),
		LastSeq:     status.LastSeq,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Origin:      CloneRunOrigin(status.RunOrigin),
	}
	if status.State == contracts.RunLoopStateWaitingSubmit {
		if lister, ok := runs.(contracts.ActiveAwaitingLister); ok {
			for _, awaiting := range lister.ActiveAwaitings(runID) {
				if !strings.EqualFold(strings.TrimSpace(awaiting.Mode), "question") {
					continue
				}
				publicID := strings.TrimSpace(awaiting.PublicAwaitingID)
				if publicID == "" {
					publicID = strings.TrimSpace(awaiting.AwaitingID)
				}
				snapshot.Awaiting = &contracts.RunAwaiting{
					AwaitingID: publicID,
					Mode:       "question",
					Questions:  append([]any(nil), awaiting.Questions...),
				}
				break
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
			if snapshot.Awaiting != nil && snapshot.Awaiting.Payload == nil && event.String("awaitingId") == snapshot.Awaiting.AwaitingID {
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
