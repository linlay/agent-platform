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
