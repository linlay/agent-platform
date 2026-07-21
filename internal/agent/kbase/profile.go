package kbase

import (
	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/contracts"
)

const (
	Mode            = "KBASE"
	MainStage       = "kbase"
	MainCacheKey    = "kbase:main"
	CreatePrefix    = "kbase"
	DefaultIconName = "kbase"
)

var defaultContextTags = []string{"system", "session"}

var defaultBudget = map[string]any{
	"timeout":  900,
	"maxSteps": 40,
	"tool": map[string]any{
		"maxCalls": 80,
	},
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
			RunAsChild: true,
		},
	}
}
