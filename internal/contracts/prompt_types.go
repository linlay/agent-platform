package contracts

import "agent-platform-runner-go/internal/api"

type PromptAppendConfig struct {
	Skill SkillAppendConfig
	Tool  ToolAppendConfig
}

type SkillAppendConfig struct {
	CatalogHeader      string
	DisclosureHeader   string
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
			CatalogHeader:    "可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:",
			DisclosureHeader: "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:",
			InstructionsPrompt: `You are a skill-driven assistant. The system provides a set of installed skills listed in the catalog below.

Skill Dispatch Rules:
1. Determine applicability: Before responding, check whether the user's request falls within the scope of any available skill. Use each skill's description to decide if it should be invoked.
2. Do not fabricate skills: Only invoke skills listed in the catalog. Never invent skill names, parameters, or capabilities that do not exist.
3. Read skill documentation first: When a skill's full instructions are disclosed, follow its parameter formats, behavioral directives, and output constraints precisely.
4. Gather missing inputs: If a skill requires parameters the user has not provided, ask the user to supply them before invoking the skill.
5. Multi-skill coordination: When a task requires multiple skills, invoke them in dependency order - complete prerequisites before dependents - then synthesize a unified response.
6. Graceful fallback: If no skill matches the request, respond using your general capabilities. Do not force a skill invocation when none is appropriate.`,
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
	AgentDigests   []AgentDigest
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

type AgentDigest struct {
	Key         string
	Name        string
	Role        string
	Description string
	Mode        string
	ModelKey    string
	Tools       []string
	Skills      []string
	Sandbox     *SandboxDigest
}

type SandboxDigest struct {
	EnvironmentID string
	Level         string
}

type LocalPaths struct {
	RuntimeHome        string
	WorkingDirectory   string
	RootDir            string
	PanDir             string
	AgentDir           string
	AgentsDir          string
	TeamsDir           string
	ChatsDir           string
	MemoryDir          string
	DataDir            string
	SkillsDir          string
	SkillsMarketDir    string
	SchedulesDir       string
	OwnerDir           string
	ModelsDir          string
	ProvidersDir       string
	MCPServersDir      string
	ViewportServersDir string
	ToolsDir           string
	ViewportsDir       string
	ChatAttachmentsDir string
}

type SandboxPaths struct {
	Cwd                string
	WorkspaceDir       string
	RootDir            string
	SkillsDir          string
	SkillsMarketDir    string
	PanDir             string
	AgentDir           string
	OwnerDir           string
	AgentsDir          string
	TeamsDir           string
	SchedulesDir       string
	ChatsDir           string
	MemoryDir          string
	ModelsDir          string
	ProvidersDir       string
	MCPServersDir      string
	ViewportServersDir string
	ToolsDir           string
	ViewportsDir       string
}
