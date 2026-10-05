package plantasks

import (
	"strings"

	"agent-platform/internal/contracts"
)

func NormalizePlanTaskStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "init":
		return "init"
	case "in_progress", "in-progress", "inprogress":
		return "in_progress"
	case "completed", "complete":
		return "completed"
	case "failed", "fail":
		return "failed"
	case "canceled", "cancelled", "cancel":
		return "canceled"
	default:
		return ""
	}
}

func PlanTasksArray(state *contracts.PlanRuntimeState) []map[string]any {
	if state == nil {
		return []map[string]any{}
	}
	tasks := make([]map[string]any, 0, len(state.Tasks))
	for _, task := range state.Tasks {
		tasks = append(tasks, map[string]any{
			"taskId":      task.TaskID,
			"description": task.Description,
			"status":      task.Status,
		})
	}
	return tasks
}
