package coder

import (
	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/contracts"
)

const (
	Mode             = "CODER"
	MainStage        = "coder"
	PlanningStage    = "coder-planning"
	ExecuteStage     = "coder-execute"
	MainCacheKey     = "coder:main"
	PlanningCacheKey = "coder:planning"
	ExecuteCacheKey  = "coder:execute"
	CreatePrefix     = "coder"
	DefaultIconName  = "coder"
)

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
	"timeout":  3600,
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

func Descriptor() agentcontract.ModeDescriptor {
	return agentcontract.ModeDescriptor{
		Mode:         Mode,
		MainStage:    MainStage,
		MainCacheKey: MainCacheKey,
		CreatePrefix: CreatePrefix,
		Profile: agentcontract.ModeProfile{
			IconName:    DefaultIconName,
			ToolNames:   DefaultToolNames(),
			ContextTags: DefaultContextTags(),
			Budget:      DefaultBudget(),
		},
		Capabilities: agentcontract.ModeCapabilities{
			InvokeChildren:  true,
			RunAsChild:      true,
			FileChangeHooks: true,
		},
	}
}
