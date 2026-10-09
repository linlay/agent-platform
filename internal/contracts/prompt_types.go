package contracts

import "agent-platform/internal/api"

type PromptAppendConfig struct {
	Skill     SkillAppendConfig
	Tool      ToolAppendConfig
	Reference ReferenceAppendConfig
}

// ReferenceAppendConfig has no source defaults: an empty prompt omits its
// section. AdvancedProtocolPrompt is added on top of ProtocolPrompt when the
// advanced user prompt wrapper is on, because replayed history still uses the
// plain [References] format.
type ReferenceAppendConfig struct {
	ProtocolPrompt         string
	AdvancedProtocolPrompt string
}

type SkillAppendConfig struct {
	CatalogHeader    string
	DisclosureHeader string
	// InstructionsPrompt has no source default: empty omits the skill rules.
	InstructionsPrompt string
	InstructionsLabel  string
}

type ToolAppendConfig struct {
	ToolDescriptionTitle string
	AfterCallHintTitle   string
}

// DefaultPromptAppendConfig holds bare section labels only; the wording lives in
// agent-prompt.yml.
func DefaultPromptAppendConfig() PromptAppendConfig {
	return PromptAppendConfig{
		Skill: SkillAppendConfig{
			CatalogHeader:     "Available skills:",
			DisclosureHeader:  "Skill instructions:",
			InstructionsLabel: "instructions",
		},
		Tool: ToolAppendConfig{
			ToolDescriptionTitle: "Tool description:",
			AfterCallHintTitle:   "After-call hints:",
		},
	}
}

// KBaseModePrompts are the configured parts added around a dedicated KBASE
// Agent's system prompt. They have no source defaults.
type KBaseModePrompts struct {
	Capability string
	Workspace  string
	Editing    string
}

type RuntimeRequestContext struct {
	AgentKey       string
	TeamID         string
	Role           string
	ChatName       string
	LocalMode      bool
	Scene          *api.Scene
	References     []api.Reference
	AuthIdentity   *AuthIdentity
	LocalPaths     LocalPaths
	SandboxPaths   SandboxPaths
	SandboxContext *SandboxContext
}

type AuthIdentity struct {
	Subject   string
	DeviceID  string
	Scope     string
	IssuedAt  string
	ExpiresAt string
}

type SandboxContext struct {
	EnvironmentID           string
	ConfiguredEnvironmentID string
	DefaultEnvironmentID    string
	Level                   string
	ContainerHubEnabled     bool
	UsesSandboxBash         bool
	ExtraMounts             []string
	EnvironmentPrompt       string
}

type LocalPaths struct {
	RuntimeHome         string
	WorkspaceDir        string
	ChatDir             string
	RootDir             string
	PanDir              string
	AgentDir            string
	AgentsDir           string
	RUAgentsDir         string
	TeamsDir            string
	ChatsDir            string
	MemoryDir           string
	SkillsDir           string
	SkillsCenterDir     string
	AutomationsDir      string
	OwnerDir            string
	ModelsDir           string
	ProvidersDir        string
	ConnectorsDir       string
	ConnectorsCenterDir string
	ToolsDir            string
}

type SandboxPaths struct {
	WorkspaceDir        string
	ChatDir             string
	RootDir             string
	SkillsDir           string
	SkillsCenterDir     string
	PanDir              string
	AgentDir            string
	OwnerDir            string
	AgentsDir           string
	RUAgentsDir         string
	TeamsDir            string
	AutomationsDir      string
	ChatsDir            string
	MemoryDir           string
	ModelsDir           string
	ProvidersDir        string
	ConnectorsDir       string
	ConnectorsCenterDir string
	ToolsDir            string
}
