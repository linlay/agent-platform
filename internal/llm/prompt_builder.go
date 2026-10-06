package llm

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/referenceprompt"
)

const agentsPromptMaxChars = 12000

type PromptBuildOptions struct {
	Stage                   string
	StageInstructionsPrompt string
	StageSystemPrompt       string
	ToolDefinitions         []api.ToolDetailResponse
	IncludeAfterCallHints   bool
}

type systemPromptSection struct {
	ID       string
	Title    string
	Category string
	Content  string
}

func buildSystemPrompt(session QuerySession, req api.QueryRequest, _ string, options PromptBuildOptions) string {
	sections := buildSystemPromptSections(session, req, options)
	contents := make([]string, 0, len(sections))
	for _, section := range sections {
		contents = append(contents, section.Content)
	}
	return joinPromptSections(contents...)
}

func buildSystemPromptSections(session QuerySession, req api.QueryRequest, options PromptBuildOptions) []systemPromptSection {
	appendConfig := effectivePromptAppendConfig(session.PromptAppend)
	stageInstructionsPrompt := strings.TrimSpace(options.StageInstructionsPrompt)
	if stageInstructionsPrompt == "" {
		stageInstructionsPrompt = resolveStageInstructionsPrompt(session, options.Stage)
	}
	stageSystemPrompt := strings.TrimSpace(options.StageSystemPrompt)
	if stageSystemPrompt == "" {
		stageSystemPrompt = resolveStageSystemPrompt(session, options.Stage)
	}

	sections := make([]systemPromptSection, 0, 16)
	appendSection := func(id, title, category, content string) {
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
		sections = append(sections, systemPromptSection{
			ID:       id,
			Title:    title,
			Category: category,
			Content:  content,
		})
	}

	toolNames := toolNamesFromDefinitions(options.ToolDefinitions, session.ToolNames)
	appendSection("agent-identity", "Agent Identity", "agent.identity", buildAgentIdentitySection(session))
	appendSection("mode-system", "Mode System Prompt", "agent.mode", agentbuiltin.RenderSystemPrompt(session, req, toolNames, options.Stage))
	for index, prompt := range session.CapabilityPrompts {
		appendSection(fmt.Sprintf("agent-capability-%d", index), "Agent Capability Prompt", "agent.capability", prompt)
	}
	appendSection("agent-soul", "Soul Prompt", "agent.soul", strings.TrimSpace(session.SoulPrompt))
	appendSection("agent-prompt", "Agent Prompt", "agent.prompt", strings.TrimSpace(session.AgentsPrompt))
	appendSection("workspace-agents", "Workspace AGENTS.md", "workspace.agents", buildWorkspaceAgentsSection(session.WorkspaceAgentsPrompt))
	appendSection("reference-protocol", "Reference Context Protocol", "references.protocol", referenceprompt.SystemPrompt)
	if session.AdvancedUserPrompt {
		appendSection("advanced-user-prompt-protocol", "Advanced User Prompt Protocol", "query.advanced_user_prompt", querymessages.AdvancedUserPromptSystemPrompt)
	}
	appendRuntimeSystemPromptSections(&sections, session)
	appendSection("runtime-path-policy", "Runtime Context: Path Policy", "runtime.path_policy", buildRuntimePathPolicySection(session, options.ToolDefinitions))
	appendSection("runtime-plan-tasks", "Runtime Context: Current Plan Tasks", "runtime.plan_tasks", buildPlanTaskContextSection(session, toolNames, options.Stage))
	appendSection("stage-instructions", "Stage Instructions Prompt", "stage.instructions", stageInstructionsPrompt)
	appendSection("stage-system", "Stage System Prompt", "stage.system", stageSystemPrompt)
	appendSection("skill-catalog", "Skill Catalog Prompt", "skills.catalog", strings.TrimSpace(session.SkillCatalogPrompt))
	appendSection("tool-appendix", "Tool Appendix", "tools.appendix", buildToolAppendix(options.ToolDefinitions, appendConfig, options.IncludeAfterCallHints))

	return sections
}

func appendRuntimeSystemPromptSections(sections *[]systemPromptSection, session QuerySession) {
	appendSection := func(id, title, category, content string) {
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
		*sections = append(*sections, systemPromptSection{
			ID:       id,
			Title:    title,
			Category: category,
			Content:  content,
		})
	}

	for _, tag := range session.ContextTags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "system":
			appendSection("runtime-system", "Runtime Context: System Environment", "runtime.system", buildSystemEnvironmentSection(session))
		case "session":
			appendSection("runtime-session", "Runtime Context: Session", "runtime.session", buildSessionSection(session))
		case "owner":
			appendSection("runtime-owner", "Runtime Context: Owner", "runtime.owner", buildSessionOwnerSection(session))
		case "agents":
			appendSection("runtime-agents", "Runtime Context: Sub-Agent Candidates", "runtime.agents", buildAgentsSection(session.RuntimeContext.AgentDigests))
		}
	}
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSection("runtime-sandbox", "Runtime Context: Sandbox", "runtime.sandbox", buildSandboxSection(session.RuntimeContext.SandboxContext))
	}
	if session.AgentHasMemoryConfig {
		appendRuntimeMemorySystemPromptSections(sections, session)
	}
}

func appendRuntimeMemorySystemPromptSections(sections *[]systemPromptSection, session QuerySession) {
	if content := buildMemorySection(session); content != "" {
		*sections = append(*sections, systemPromptSection{ID: "memory-personal", Title: "Personal Memory", Category: "memory.personal", Content: content})
	}
}

func buildRuntimePathPolicySection(session QuerySession, definitions []api.ToolDetailResponse) string {
	if sessionHasWorkspace(session) {
		return ""
	}
	tools := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if name := normalizedToolDefinitionName(definition); name != "" {
			tools[name] = struct{}{}
		}
	}
	hasTool := func(names ...string) bool {
		for _, name := range names {
			if _, ok := tools[name]; ok {
				return true
			}
		}
		return false
	}
	hasSkills := strings.TrimSpace(session.SkillCatalogPrompt) != ""
	if len(tools) == 0 && !hasSkills {
		return ""
	}
	hasPathTools := hasTool(
		"bash",
		"file_read",
		"file_write",
		"file_edit",
		"file_glob",
		"file_grep",
		"artifact_publish",
		"vision_recognize",
	)
	if !hasPathTools && !hasSkills {
		return ""
	}

	lines := []string{
		"Runtime Context: Path Policy",
		"- Workspace is unavailable. The current Chat is a separate semantic root and is never an implicit Workspace fallback.",
	}
	if hasTool("bash") {
		lines = append(lines, `- Every bash call must pass an explicit cwd. Use cwd: "@chat" for Chat files or cwd: "@temp" for temporary work.`)
	}
	if hasTool("file_glob", "file_grep") {
		lines = append(lines, `- file_glob and file_grep must pass an explicit path, normally "@chat" or "@temp".`)
	}
	if hasTool("file_read", "file_write", "file_edit", "artifact_publish", "vision_recognize") {
		lines = append(lines, "- File paths must use an explicit semantic root such as @chat, @agent, @skills, @skills-center, @connectors, @owner, or @temp, or an allowed absolute path. Relative paths and @workspace fail with workspace_unavailable.")
	}
	if hasSkills {
		lines = append(lines, "- Load an applicable skill with file_read using the exact path in its catalog entry (@skills or @connectors). Do not search or traverse directories to discover its location.")
	}
	return strings.Join(lines, "\n")
}

func buildPlanTaskContextSection(session QuerySession, toolNames []string, stage string) string {
	context := strings.TrimSpace(session.PlanTaskContext)
	if context == "" || !shouldUsePlanTaskContextForStage(stage, toolNames, session.PlanningMode) {
		return ""
	}
	return context
}

func toolNamesFromDefinitions(definitions []api.ToolDetailResponse, fallback []string) []string {
	if len(definitions) == 0 {
		return append([]string(nil), fallback...)
	}
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			name = strings.TrimSpace(definition.Key)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func localTimezoneName() string {
	tz := time.Local.String()
	if tz == "Local" {
		if zone := strings.TrimSpace(os.Getenv("TZ")); zone != "" {
			tz = zone
		}
	}
	return tz
}

func buildAgentIdentitySection(session QuerySession) string {
	lines := []string{"Agent Identity"}
	// TEAM executes through a synthetic AgentKey so it can reuse the ordinary
	// engine contract. That key is deliberately runtime-only and must not enter
	// persisted prompts or any model-visible/public diagnostics.
	if session.TeamRuntime == nil {
		appendKeyValue(&lines, "key", session.AgentKey)
	}
	appendKeyValue(&lines, "name", session.AgentName)
	appendKeyValue(&lines, "role", session.AgentRole)
	appendKeyValue(&lines, "description", session.AgentDescription)
	appendKeyValue(&lines, "mode", session.Mode)
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func buildWorkspaceAgentsSection(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if strings.HasPrefix(prompt, "Workspace ") || strings.HasPrefix(prompt, "Agent-managed Project ") {
		return prompt
	}
	return "Workspace AGENTS.md\n" + prompt
}

func effectivePromptAppendConfig(config PromptAppendConfig) PromptAppendConfig {
	defaults := DefaultPromptAppendConfig()
	if strings.TrimSpace(config.Skill.InstructionsPrompt) != "" {
		defaults.Skill.InstructionsPrompt = strings.TrimSpace(config.Skill.InstructionsPrompt)
	}
	if strings.TrimSpace(config.Skill.CatalogHeader) != "" {
		defaults.Skill.CatalogHeader = strings.TrimSpace(config.Skill.CatalogHeader)
	}
	if strings.TrimSpace(config.Skill.DisclosureHeader) != "" {
		defaults.Skill.DisclosureHeader = strings.TrimSpace(config.Skill.DisclosureHeader)
	}
	if strings.TrimSpace(config.Skill.InstructionsLabel) != "" {
		defaults.Skill.InstructionsLabel = strings.TrimSpace(config.Skill.InstructionsLabel)
	}
	if strings.TrimSpace(config.Tool.ToolDescriptionTitle) != "" {
		defaults.Tool.ToolDescriptionTitle = strings.TrimSpace(config.Tool.ToolDescriptionTitle)
	}
	if strings.TrimSpace(config.Tool.AfterCallHintTitle) != "" {
		defaults.Tool.AfterCallHintTitle = strings.TrimSpace(config.Tool.AfterCallHintTitle)
	}
	return defaults
}

func resolveStageInstructionsPrompt(session QuerySession, stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "plan":
		return strings.TrimSpace(session.PlanPrompt)
	case "execute":
		if strings.TrimSpace(session.ExecutePrompt) != "" {
			return strings.TrimSpace(session.ExecutePrompt)
		}
	case "summary":
		if strings.TrimSpace(session.SummaryPrompt) != "" {
			return strings.TrimSpace(session.SummaryPrompt)
		}
	}
	return ""
}

func resolveStageSystemPrompt(session QuerySession, stage string) string {
	if agentcoder.IsMode(session.Mode) && (session.PlanningMode || strings.HasPrefix(strings.ToLower(strings.TrimSpace(stage)), "coder-")) {
		if strings.Contains(strings.ToLower(strings.TrimSpace(stage)), "planning") {
			return strings.TrimSpace(session.ResolvedCoderPlanningSettings.Planning.SystemPrompt)
		}
		return strings.TrimSpace(session.ResolvedCoderPlanningSettings.Execute.SystemPrompt)
	}
	settings := session.ResolvedPlanExecuteSettings
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "plan":
		return strings.TrimSpace(settings.Plan.SystemPrompt)
	case "summary":
		return strings.TrimSpace(settings.Summary.SystemPrompt)
	case "execute":
		return strings.TrimSpace(settings.Execute.SystemPrompt)
	default:
		return strings.TrimSpace(settings.Execute.SystemPrompt)
	}
}

func buildSystemEnvironmentSection(session QuerySession) string {
	header := config.RuntimePromptConfig{EnvironmentPromptTemplate: session.EnvironmentPromptTemplate}.Render(
		runtime.GOOS, runtime.GOARCH, localTimezoneName(), session.Locale)
	lines := []string{header}
	appendContextPaths(&lines, session)
	return strings.Join(lines, "\n")
}

func buildSessionSection(session QuerySession) string {
	lines := []string{"Runtime Context: Session"}
	appendKeyValue(&lines, "chatId", session.ChatID)
	appendKeyValue(&lines, "teamId", session.RuntimeContext.TeamID)
	if summary := summarizeScene(session.RuntimeContext.Scene); summary != "" {
		lines = append(lines, "scene: "+summary)
	}
	if identity := session.RuntimeContext.AuthIdentity; identity != nil {
		appendKeyValue(&lines, "subject", identity.Subject)
		appendKeyValue(&lines, "deviceId", identity.DeviceID)
		appendKeyValue(&lines, "scope", identity.Scope)
		appendKeyValue(&lines, "issuedAt", identity.IssuedAt)
		appendKeyValue(&lines, "expiresAt", identity.ExpiresAt)
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func appendContextPaths(lines *[]string, session QuerySession) {
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSandboxContextPaths(lines, session.RuntimeContext.SandboxPaths, session.RuntimeContext.LocalMode)
		return
	}
	appendLocalContextPaths(lines, session.RuntimeContext.LocalPaths)
}

func appendSandboxContextPaths(lines *[]string, paths SandboxPaths, localMode bool) {
	rootDirDesc := "Container home directory"
	panDirDesc := "Mounted user drive"
	if localMode {
		rootDirDesc = "Root directory"
		panDirDesc = "User drive directory"
	}
	appendSemanticRoot(lines, "workspace_dir", paths.WorkspaceDir, "Relative path base / permission workspace root")
	appendSemanticRoot(lines, "chat_dir", paths.ChatDir, "Current chat files, artifacts, temporary code and files")
	appendContextDir(lines, "root_dir", paths.RootDir, rootDirDesc)
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "Current Agent private skills")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "Current Agent runtime directory")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "Owner profile directory")
	appendContextDir(lines, "skills_center_dir", paths.SkillsCenterDir, "Shared skills catalog")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "Editable Agent source directory")
	appendContextDir(lines, "ru_agents_dir", paths.RUAgentsDir, "Platform-generated Agent execution directory; do not edit manually")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "Team configuration directory")
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "Automation configuration directory")
	appendContextDir(lines, "chats_dir", paths.ChatsDir, "Chat records directory")
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "Memory storage directory")
	appendContextDir(lines, "models_dir", paths.ModelsDir, "Model registry directory")
	appendContextDir(lines, "providers_dir", paths.ProvidersDir, "Provider registry directory")
	appendContextDir(lines, "connectors_center_dir", paths.ConnectorsCenterDir, "External connector package sources")
	appendContextDir(lines, "connectors_dir", paths.ConnectorsDir, "Mounted connectors resolved to shared read-only packages from the Run snapshot; use full paths listed in the skill catalog")
	appendContextDir(lines, "pan_dir", paths.PanDir, panDirDesc)
}

func appendLocalContextPaths(lines *[]string, paths LocalPaths) {
	appendSemanticRoot(lines, "workspace_dir", paths.WorkspaceDir, "Relative path base / permission workspace root")
	appendSemanticRoot(lines, "chat_dir", paths.ChatDir, "Current chat files, artifacts, temporary code and files")
	appendContextDir(lines, "root_dir", paths.RootDir, "Root directory")
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "Current Agent private skills")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "Current Agent runtime directory")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "Owner profile directory")
	appendContextDir(lines, "skills_center_dir", paths.SkillsCenterDir, "Shared skills catalog")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "Editable Agent source directory")
	appendContextDir(lines, "ru_agents_dir", paths.RUAgentsDir, "Platform-generated Agent execution directory; do not edit manually")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "Team configuration directory")
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "Automation configuration directory")
	appendContextDir(lines, "chats_dir", paths.ChatsDir, "Chat records directory")
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "Memory storage directory")
	appendContextDir(lines, "models_dir", paths.ModelsDir, "Model registry directory")
	appendContextDir(lines, "providers_dir", paths.ProvidersDir, "Provider registry directory")
	appendContextDir(lines, "connectors_center_dir", paths.ConnectorsCenterDir, "External connector package sources")
	appendContextDir(lines, "connectors_dir", paths.ConnectorsDir, "Mounted connectors resolved to shared read-only packages from the Run snapshot; use full paths listed in the skill catalog")
	appendContextDir(lines, "pan_dir", paths.PanDir, "User drive directory")
}

func appendSemanticRoot(lines *[]string, key, value, desc string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "unavailable"
	}
	*lines = append(*lines, key+": "+value+" # "+desc)
}

// appendContextDir adds a dir entry only if mounted (non-empty), with a description.
func appendContextDir(lines *[]string, key, value, desc string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	*lines = append(*lines, key+": "+strings.TrimSpace(value)+" # "+desc)
}

func buildSessionOwnerSection(session QuerySession) string {
	if !session.OwnerPromptLoaded {
		return buildOwnerSection(session.RuntimeContext.LocalPaths)
	}
	if strings.TrimSpace(session.OwnerPrompt) == "" {
		return ""
	}
	return "Runtime Context: Owner\n<owner_data>\n" + session.OwnerPrompt + "\n</owner_data>"
}

func buildOwnerSection(paths LocalPaths) string {
	if paths.OwnerDir == "" {
		return ""
	}
	d, err := memory.NewStore("", paths.OwnerDir, nil).Read("owner", "")
	if err != nil || strings.TrimSpace(d.Content) == "" {
		return ""
	}
	return "Runtime Context: Owner\n<owner_data>\n" + d.Content + "\n</owner_data>"
}

func buildSandboxSection(context *SandboxContext) string {
	if context == nil || strings.TrimSpace(context.EnvironmentPrompt) == "" {
		return ""
	}
	lines := []string{"Runtime Context: Sandbox"}
	appendKeyValue(&lines, "environmentId", context.EnvironmentID)
	appendKeyValue(&lines, "defaultEnvironmentId", context.DefaultEnvironmentID)
	appendKeyValue(&lines, "level", context.Level)
	if len(context.ExtraMounts) > 0 {
		lines = append(lines, "sandboxMounts:")
		for _, mount := range context.ExtraMounts {
			if strings.TrimSpace(mount) != "" {
				lines = append(lines, "- "+strings.TrimSpace(mount))
			}
		}
	}
	lines = append(lines, "environment_prompt:")
	lines = append(lines, strings.TrimSpace(context.EnvironmentPrompt))
	return strings.Join(lines, "\n")
}

func buildAgentsSection(digests []AgentDigest) string {
	if len(digests) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(digests))
	totalChars := 0
	included := 0
	total := 0
	for _, digest := range digests {
		if strings.TrimSpace(digest.Key) != "" {
			total++
		}
	}
	for _, digest := range digests {
		if strings.TrimSpace(digest.Key) == "" {
			continue
		}
		block := formatAgentDigest(digest)
		if strings.TrimSpace(block) == "" {
			continue
		}
		projected := totalChars + len(block)
		if len(blocks) > 0 {
			projected += len("\n---\n")
		}
		if projected > agentsPromptMaxChars {
			break
		}
		blocks = append(blocks, block)
		totalChars = projected
		included++
	}
	if len(blocks) == 0 {
		return ""
	}
	builder := strings.Builder{}
	builder.WriteString("Runtime Context: Sub-Agent Candidates\n")
	builder.WriteString("以下是为当前智能体选择的可调用/委派子智能体候选摘要，仅供目标选择和任务路由参考。\n")
	builder.WriteString("这些候选不是当前智能体，也不构成 agent_invoke、agent_delegate、chat_start 或 catalog 的权限或目标白名单；当前智能体及其 key 只由 Agent Identity 定义。\n")
	builder.WriteString("如需了解某个候选的完整配置，可以自行查看 agents 目录下对应的 agent.yml。\n")
	builder.WriteString(strings.Join(blocks, "\n---\n"))
	if included < total {
		builder.WriteString(fmt.Sprintf("\n[TRUNCATED: agents exceeds max chars=%d, included=%d/%d]", agentsPromptMaxChars, included, total))
	}
	return builder.String()
}

func formatAgentDigest(digest AgentDigest) string {
	lines := []string{}
	appendKeyValue(&lines, "key", digest.Key)
	appendKeyValue(&lines, "name", digest.Name)
	appendKeyValue(&lines, "role", digest.Role)
	appendKeyValue(&lines, "description", digest.Description)
	return strings.Join(lines, "\n")
}

func buildMemorySection(session QuerySession) string {
	return strings.TrimSpace(session.MemoryContext)
}

func summarizeScene(scene *api.Scene) string {
	if scene == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if strings.TrimSpace(scene.Title) != "" {
		parts = append(parts, "title="+strings.TrimSpace(scene.Title))
	}
	if strings.TrimSpace(scene.URL) != "" {
		parts = append(parts, "url="+strings.TrimSpace(scene.URL))
	}
	return strings.Join(parts, ", ")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func buildToolAppendix(definitions []api.ToolDetailResponse, appendConfig PromptAppendConfig, includeAfterCallHints bool) string {
	if !includeAfterCallHints || len(definitions) == 0 {
		return ""
	}
	appendConfig = effectivePromptAppendConfig(appendConfig)
	sortedDefs := append([]api.ToolDetailResponse(nil), definitions...)
	sort.Slice(sortedDefs, func(i, j int) bool {
		return normalizePromptToolName(sortedDefs[i]) < normalizePromptToolName(sortedDefs[j])
	})

	afterCallLines := make([]string, 0, len(sortedDefs))
	seenAfterHints := map[string]struct{}{}
	for _, tool := range sortedDefs {
		name := normalizePromptToolName(tool)
		if name == "" {
			continue
		}
		if hint := strings.TrimSpace(tool.AfterCallHint); hint != "" {
			line := "- " + name + ": " + hint
			if _, ok := seenAfterHints[line]; !ok {
				seenAfterHints[line] = struct{}{}
				afterCallLines = append(afterCallLines, line)
			}
		}
	}

	if len(afterCallLines) > 0 {
		return strings.TrimSpace(appendConfig.Tool.AfterCallHintTitle) + "\n" + strings.Join(afterCallLines, "\n")
	}
	return ""
}

func normalizePromptToolName(tool api.ToolDetailResponse) string {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		name = strings.TrimSpace(tool.Key)
	}
	return strings.ToLower(name)
}

func joinPromptSections(sections ...string) string {
	filtered := make([]string, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section) != "" {
			filtered = append(filtered, strings.TrimSpace(section))
		}
	}
	return strings.Join(filtered, "\n\n")
}

func appendKeyValue(lines *[]string, key string, value string) {
	if strings.TrimSpace(value) != "" {
		*lines = append(*lines, key+": "+strings.TrimSpace(value))
	}
}
