package contracts

import (
	"encoding/json"
	"strings"
)

func NormalizeBudget(b Budget) Budget {
	return normalizeBudget(b)
}

func CloneMap(values map[string]any) map[string]any {
	return cloneAnyMap(values)
}

func CloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

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

func PlanTasksArray(state *PlanRuntimeState) []map[string]any {
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

func MarshalJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
