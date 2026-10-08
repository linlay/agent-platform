package coder

import (
	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/contracts"
)

const (
	Mode            = "CODER"
	MainStage       = "coder"
	MainCacheKey    = "coder:main"
	CreatePrefix    = "coder"
	DefaultIconName = "coder"
)

var createToolNames = []string{
	"bash",
	"file_read",
	"file_write",
	"file_edit",
	"file_glob",
	"file_grep",
	"datetime",
	"vision_recognize",
	"artifact_publish",
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

// CreateToolNames supplies recommendations for new Agent configuration.
func CreateToolNames() []string {
	return append([]string(nil), createToolNames...)
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
