package contracts

import "strings"

const InvokeAgentsToolName = "agent_invoke"

const PlanAddTasksToolName = "plan_add_tasks"
const PlanGetTasksToolName = "plan_get_tasks"
const PlanUpdateTaskToolName = "plan_update_task"

var PlanTaskToolNames = []string{
	PlanAddTasksToolName,
	PlanGetTasksToolName,
	PlanUpdateTaskToolName,
}

const MaxInvokeAgentTasks = 5

func IsPlanTaskToolName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, toolName := range PlanTaskToolNames {
		if normalized == strings.ToLower(toolName) {
			return true
		}
	}
	return false
}

func AppendPlanTaskToolNames(base []string) []string {
	return appendUniqueToolNames(base, PlanTaskToolNames...)
}

func RemovePlanTaskToolNames(base []string) []string {
	blocked := map[string]struct{}{}
	for _, toolName := range PlanTaskToolNames {
		blocked[strings.ToLower(strings.TrimSpace(toolName))] = struct{}{}
	}
	out := make([]string, 0, len(base))
	for _, name := range base {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := blocked[strings.ToLower(trimmed)]; ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

func appendUniqueToolNames(base []string, extra ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, name := range append(base, extra...) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
