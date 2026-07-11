package team

import (
	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/contracts"
)

const (
	// Mode is an internal-only mode. Catalog adapters must not accept it as an
	// ordinary AgentDefinition mode; a Team runtime creates the coordinator
	// session from a frozen Team snapshot.
	Mode            = "TEAM"
	MainStage       = "team"
	MainCacheKey    = "team:main"
	DefaultIconName = "team"

	ToolDelegate = "team_delegate"
	ToolInvoke   = "team_invoke"
	// HiddenToolSchemaVersion participates in Team snapshot/system-init
	// fingerprints. Bump it whenever either hidden tool contract changes in an
	// incompatible way.
	HiddenToolSchemaVersion = "team_delegate+team_invoke:v1"

	DelegateModeDirect = "direct"
	DelegateModeFanout = "fanout"

	DispatchKindDirect = "direct"
	DispatchKindFanout = "fanout"
	DispatchKindInvoke = "invoke"

	DefaultMaxParallel = 5
	MaxParallel        = 5
	MaxRoutingRetries  = 1
)

var defaultToolNames = []string{ToolDelegate, ToolInvoke}

var defaultContextTags = []string{"system", "session"}

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
		// TEAM cannot be created through the ordinary Agent create endpoint.
		CreatePrefix: "",
		Profile: agentcontract.ModeProfile{
			IconName:    DefaultIconName,
			ToolNames:   DefaultToolNames(),
			ContextTags: DefaultContextTags(),
			Budget:      DefaultBudget(),
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
