package team

import (
	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/contracts"
)

const (
	// Mode is a public Agent mode with run-local member delegation.
	Mode            = "TEAM"
	MainStage       = "team"
	MainCacheKey    = "team:main"
	DefaultIconName = "team"

	ToolDelegate = "agent_delegate"
	// HiddenToolSchemaVersion participates in Team snapshot/system-init
	// fingerprints. Bump it whenever the hidden tool contract changes in an
	// incompatible way.
	HiddenToolSchemaVersion = "agent_delegate:v1"

	DefaultMaxParallel = 5
	MaxParallel        = 5
)

var defaultToolNames = []string{ToolDelegate}
var defaultContextTags = []string{}

var defaultBudget = map[string]any{
	"timeout":  3600,
	"maxSteps": 60,
	"tool": map[string]any{
		"maxCalls": 40,
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
		CreatePrefix: "team",
		Profile: agentcontract.ModeProfile{
			IconName: DefaultIconName,

			Budget: DefaultBudget(),
		},
		Capabilities: agentcontract.ModeCapabilities{
			InvokeChildren: true,
		},
	}
}

func NormalizeMaxParallel(value int) int {
	if value <= 0 {
		return DefaultMaxParallel
	}
	if value > MaxParallel {
		return MaxParallel
	}
	return value
}
