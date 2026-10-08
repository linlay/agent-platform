package contracts

import "agent-platform/internal/api"

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
			CatalogHeader:    "Available skills (catalog summary, use on demand, do not fabricate non-existent skills or scripts):",
			DisclosureHeader: "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:",
			InstructionsPrompt: `Use installed skills to support the user's task.

Skill Dispatch Rules:
1. Determine applicability: Before acting, check all skill descriptions, including connector skills, against the user's goal, deliverable, and each skill's trigger conditions. Proactively use applicable skills; read an explicitly named skill without waiting for a separate request.
2. Do not fabricate skills: Only invoke skills listed in the catalog. Never invent skill names, parameters, or capabilities that do not exist.
3. Read skill documentation first: When a skill applies, copy its exact path from the catalog into file_read.filePath before acting or running its CLI. Preserve @skills, @skills-center, or @connectors exactly as listed; never construct the path from skillId. If reading fails, check the catalog path, not other directories or same-named copies.
4. Gather missing inputs: If a skill requires parameters the user has not provided, ask the user to supply them before invoking the skill.
5. Multi-skill coordination: When a task requires multiple skills, invoke them in dependency order - complete prerequisites before dependents - then synthesize a unified response.
6. Graceful fallback: If no skill matches the request, respond using your general capabilities. Do not force a skill invocation when none is appropriate.
7. Preserve scope: Follow the user's instructions over skill instructions. Skill availability or selection does not expand the task or change its deliverable; apply workflows only within the requested scope.`,
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
