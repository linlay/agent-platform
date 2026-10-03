package conversation

import (
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

// ControlArchiveIDs validates the whole request before any mutation. Single
// chatId calls keep their existing response; chatIds calls return per-item results.
func ControlArchiveIDs(args map[string]any) ([]string, bool, error) {
	raw, batch := args["chatIds"]
	if !batch {
		id, ok := args["chatId"].(string)
		if !ok || !chat.ValidChatID(strings.TrimSpace(id)) {
			return nil, false, fmt.Errorf("a valid chatId or chatIds is required")
		}
		return []string{strings.TrimSpace(id)}, false, nil
	}
	if _, exists := args["chatId"]; exists {
		return nil, true, fmt.Errorf("chatId and chatIds are mutually exclusive")
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > 100 {
		return nil, true, fmt.Errorf("chatIds must contain 1 to 100 distinct Chat IDs")
	}
	ids := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		id, ok := value.(string)
		id = strings.TrimSpace(id)
		if !ok || !chat.ValidChatID(id) || seen[id] {
			return nil, true, fmt.Errorf("chatIds must contain valid, distinct Chat IDs")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, true, nil
}

func (s *Service) controlArchiveBatch(c ControlCaller, action string, ids []string) map[string]any {
	results := make([]map[string]any, 0, len(ids))
	succeeded := 0
	for _, id := range ids {
		// Each call acquires the shared mutation lock, rechecks owner/idle state and
		// emits its existing notification. No recursive locking or whole-batch rollback.
		_, err := s.ControlManage(c, action, map[string]any{"chatId": id}, "")
		item := map[string]any{"chatId": id, "success": err == nil}
		if err == nil {
			succeeded++
			item["executionState"] = "committed"
		} else {
			item["error"] = err.Error()
			state := "unknown"
			var mutation *contracts.MutationError
			if errors.As(err, &mutation) {
				state = mutation.State
			}
			item["executionState"] = state
		}
		results = append(results, item)
	}
	return map[string]any{"total": len(ids), "succeeded": succeeded, "failed": len(ids) - succeeded, "results": results}
}
