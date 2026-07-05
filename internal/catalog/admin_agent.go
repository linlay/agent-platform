package catalog

import "agent-platform/internal/contracts"

const (
	AdminAgentStatusReady   = "ready"
	AdminAgentStatusInvalid = "invalid"
)

type AdminAgentDiagnostic struct {
	Severity   string
	Code       string
	Message    string
	SourcePath string
}

type AdminAgent struct {
	Key          string
	Name         string
	Icon         any
	Description  string
	Role         string
	Mode         string
	ModelKey     string
	Tools        []string
	Skills       []string
	Workspace    AgentWorkspaceConfig
	Controls     []map[string]any
	ChannelConfig AgentChannelConfig
	Meta         map[string]any
	Status       string
	Diagnostics  []AdminAgentDiagnostic
	Source       EditableAgentSource
	Definition   map[string]any
	SoulPrompt   string
	AgentsPrompt string
}

func cloneAdminAgent(src AdminAgent) AdminAgent {
	dst := src
	dst.Tools = append([]string(nil), src.Tools...)
	dst.Skills = append([]string(nil), src.Skills...)
	dst.Controls = cloneListMaps(src.Controls)
	dst.ChannelConfig = cloneAgentChannelConfig(src.ChannelConfig)
	dst.Diagnostics = append([]AdminAgentDiagnostic(nil), src.Diagnostics...)
	dst.Meta = contracts.CloneMap(src.Meta)
	dst.Definition = contracts.CloneMap(src.Definition)
	return dst
}
