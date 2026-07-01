package coder

import "agent-platform/internal/contracts"

const DefaultIconName = "coder"

var defaultToolNames = []string{
	"bash",
	"file_read",
	"file_write",
	"file_edit",
	"file_glob",
	"file_grep",
	"datetime",
	"regex",
	"vision_recognize",
	contracts.PlanAddTasksToolName,
	contracts.PlanGetTasksToolName,
	contracts.PlanUpdateTaskToolName,
}

var defaultContextTags = []string{"system", "session"}

var defaultBudget = map[string]any{
	"timeout":  1800,
	"maxSteps": 240,
	"tool": map[string]any{
		"maxCalls": 200,
	},
}

func DefaultToolNames() []string {
	return append([]string(nil), defaultToolNames...)
}

func DefaultContextTags() []string {
	return append([]string(nil), defaultContextTags...)
}

func DefaultBudget() map[string]any {
	return contracts.CloneMap(defaultBudget)
}
