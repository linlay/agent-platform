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

func DefaultPromptAppendConfig() PromptAppendConfig {
	return PromptAppendConfig{
		Skill: SkillAppendConfig{
			CatalogHeader:     "Available skills (catalog summary, use on demand, do not fabricate non-existent skills or scripts):",
			DisclosureHeader:  "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:",
			InstructionsLabel: "instructions",
		},
		Tool: ToolAppendConfig{
			ToolDescriptionTitle: "工具说明:",
			AfterCallHintTitle:   "工具调用后推荐指令:",
		},
	}
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
