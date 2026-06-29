package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/referenceprompt"
)

const allAgentsPromptMaxChars = 12000

const defaultKBaseSystemPrompt = `KBASE Mode
You answer using the workspace knowledge base for this agent.

Rules:
- Search the knowledge base with kbase_search before answering factual questions about the indexed workspace.
- Base answers on retrieved evidence. If the available evidence is insufficient, say that the knowledge base does not contain enough information.
- Cite source paths and line ranges from kbase_search or kbase_read when giving concrete claims.
- Use kbase_read when a search result needs more surrounding context.
- Do not claim that unindexed or missing files were searched.`

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
	appendSection("coder-system", "Coder System Prompt", "coder.system", buildCoderSystemPromptSection(session, req, toolNames, options.Stage))
	appendSection("kbase-system", "KBASE System Prompt", "kbase.system", buildKBaseSystemPromptSection(session, req, toolNames, options.Stage))
	appendSection("agent-soul", "Soul Prompt", "agent.soul", strings.TrimSpace(session.SoulPrompt))
	appendSection("agent-prompt", "Agent Prompt", "agent.prompt", strings.TrimSpace(session.AgentsPrompt))
	appendSection("workspace-agents", "Workspace AGENTS.md", "workspace.agents", buildWorkspaceAgentsSection(session.WorkspaceAgentsPrompt))
	appendSection("static-memory", "Static Memory Prompt", "memory.static", strings.TrimSpace(session.StaticMemoryPrompt))
	appendSection("reference-protocol", "Reference Context Protocol", "references.protocol", referenceprompt.SystemPrompt)
	if session.AdvancedUserPrompt {
		appendSection("advanced-user-prompt-protocol", "Advanced User Prompt Protocol", "query.advanced_user_prompt", querymessages.AdvancedUserPromptSystemPrompt)
	}
	appendRuntimeSystemPromptSections(&sections, session, req)
	appendSection("stage-instructions", "Stage Instructions Prompt", "stage.instructions", stageInstructionsPrompt)
	appendSection("stage-system", "Stage System Prompt", "stage.system", stageSystemPrompt)
	appendSection("skill-catalog", "Skill Catalog Prompt", "skills.catalog", strings.TrimSpace(session.SkillCatalogPrompt))
	appendSection("tool-appendix", "Tool Appendix", "tools.appendix", buildToolAppendix(options.ToolDefinitions, appendConfig, options.IncludeAfterCallHints))

	return sections
}

func buildKBaseSystemPromptSection(session QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), "KBASE") {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stage), "kbase") {
		return ""
	}
	prompt := strings.TrimSpace(session.KBaseSystemPrompt)
	if prompt == "" {
		prompt = defaultKBaseSystemPrompt
	}
	return renderCoderPromptTemplate(prompt, coderPromptTemplateValues(session, req, coderPromptTemplateData{
		AvailableTools: toolNames,
	}))
}

func appendRuntimeSystemPromptSections(sections *[]systemPromptSection, session QuerySession, req api.QueryRequest) {
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
			appendSection("runtime-session", "Runtime Context: Session", "runtime.session", buildSessionSection(session, req))
		case "owner":
			appendSection("runtime-owner", "Runtime Context: Owner", "runtime.owner", buildOwnerSection(session.RuntimeContext.LocalPaths))
		case "all-agents":
			appendSection("runtime-all-agents", "Runtime Context: All Agents", "runtime.all_agents", buildAllAgentsSection(session.RuntimeContext.AgentDigests))
		}
	}
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSection("runtime-sandbox", "Runtime Context: Sandbox", "runtime.sandbox", buildSandboxSection(session.RuntimeContext.SandboxContext))
	}
	if session.AgentHasMemoryConfig {
		appendRuntimeMemorySystemPromptSections(sections, session, req)
	}
}

func appendRuntimeMemorySystemPromptSections(sections *[]systemPromptSection, session QuerySession, req api.QueryRequest) {
	before := len(*sections)
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
	appendSection("memory-stable", "Runtime Context: Stable Memory", "memory.stable", strings.TrimSpace(session.StableMemoryContext))
	appendSection("memory-session", "Runtime Context: Current Session", "memory.session", strings.TrimSpace(session.SessionMemoryContext))
	appendSection("memory-observation", "Runtime Context: Relevant Observations", "memory.observation", strings.TrimSpace(session.ObservationContext))
	appendSection("memory-workflow", "Runtime Context: Workflow Memory", "memory.workflow", strings.TrimSpace(session.WorkflowContext))
	if len(*sections) == before {
		appendSection("memory-agent", "Runtime Context: Agent Memory", "memory.agent", buildMemorySection(session, req))
	}
}

func buildCoderSystemPromptSection(session QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), "CODER") {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stage), "coder") {
		return ""
	}
	return renderCoderPromptTemplate(session.CoderSystemPrompt, coderPromptTemplateValues(session, req, coderPromptTemplateData{
		AvailableTools:    toolNames,
		PlanStageTools:    coderPlanningModePlanTools,
		ExecuteStageTools: removeToolNames(AppendPlanTaskToolNames(toolNames), FinalizePlanningToolName, "ask_user_question"),
	}))
}

type coderPromptTemplateData struct {
	AvailableTools          []string
	PlanStageTools          []string
	ExecuteStageTools       []string
	ExecuteToolDescriptions string
}

func renderCoderPromptTemplate(prompt string, values map[string]string) string {
	return strings.TrimSpace(renderTemplate(prompt, values))
}

func coderPromptTemplateValues(session QuerySession, req api.QueryRequest, data coderPromptTemplateData) map[string]string {
	availableTools := data.AvailableTools
	if len(availableTools) == 0 {
		availableTools = session.ToolNames
	}
	planStageTools := data.PlanStageTools
	if len(planStageTools) == 0 {
		planStageTools = coderPlanningModePlanTools
	}
	executeStageTools := data.ExecuteStageTools
	if len(executeStageTools) == 0 {
		executeStageTools = removeToolNames(AppendPlanTaskToolNames(availableTools), FinalizePlanningToolName, "ask_user_question")
	}
	workspaceDir := firstNonBlank(
		session.RuntimeContext.LocalPaths.WorkspaceDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
		session.WorkspaceRoot,
	)
	chatDir := firstNonBlank(
		session.RuntimeContext.LocalPaths.ChatAttachmentsDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
	)
	return map[string]string{
		"agent_key":                   session.AgentKey,
		"agent_name":                  session.AgentName,
		"mode":                        session.Mode,
		"planning_mode":               fmt.Sprintf("%t", session.PlanningMode),
		"workspace_dir":               workspaceDir,
		"chat_dir":                    chatDir,
		"current_date":                time.Now().Format("2006-01-02"),
		"timezone":                    localTimezoneName(),
		"language_preference":         "中文",
		"available_tools":             strings.Join(normalizeToolNameList(availableTools), ", "),
		"plan_stage_tools":            strings.Join(normalizeToolNameList(planStageTools), ", "),
		"execute_stage_tools":         strings.Join(normalizeToolNameList(executeStageTools), ", "),
		"execute_tool_descriptions":   strings.TrimSpace(data.ExecuteToolDescriptions),
		"ask_user_question_tool_name": "ask_user_question",
		"finalize_planning_tool_name": FinalizePlanningToolName,
		"bash_tool_name":              "bash",
		"datetime_tool_name":          "datetime",
		"file_read_tool_name":         "file_read",
		"file_glob_tool_name":         "file_glob",
		"file_grep_tool_name":         "file_grep",
		"file_write_tool_name":        "file_write",
		"file_edit_tool_name":         "file_edit",
		"agent_tool_name":             InvokeAgentsToolName,
		"user_request":                req.Message,
	}
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

func normalizeToolNameList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
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
	appendKeyValue(&lines, "key", session.AgentKey)
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
	settings := session.ResolvedStageSettings
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
	lines := []string{
		"Runtime Context: System Environment",
		"os: " + runtime.GOOS,
		"arch: " + runtime.GOARCH,
		"timezone: " + localTimezoneName(),
		"language: 中文",
	}
	appendContextPaths(&lines, session)
	return strings.Join(lines, "\n")
}

func buildSessionSection(session QuerySession, req api.QueryRequest) string {
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
	rootDirDesc := "容器家目录"
	panDirDesc := "用户网盘挂载目录"
	if localMode {
		rootDirDesc = "root 目录"
		panDirDesc = "用户网盘目录"
	}
	appendContextDir(lines, "workspace_dir", paths.WorkspaceDir, "工具默认工作目录 / 权限工作根")
	appendContextDir(lines, "chat_dir", paths.WorkspaceDir, "当前会话文件目录，可存放产物、临时代码和临时文件")
	appendContextDir(lines, "root_dir", paths.RootDir, rootDirDesc)
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "当前 agent 私有技能目录")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "当前 agent 定义目录")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "owner 用户档案目录")
	appendContextDir(lines, "skills_market_dir", paths.SkillsMarketDir, "共享技能市场目录")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "全部 agent 定义目录")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "团队配置目录")
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "计划任务配置目录")
	appendContextDir(lines, "chats_dir", paths.ChatsDir, "会话记录目录")
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "记忆存储目录")
	appendContextDir(lines, "models_dir", paths.ModelsDir, "模型注册配置目录")
	appendContextDir(lines, "providers_dir", paths.ProvidersDir, "供应商注册配置目录")
	appendContextDir(lines, "mcp_servers_dir", paths.MCPServersDir, "MCP 服务注册目录")
	appendContextDir(lines, "viewport_servers_dir", paths.ViewportServersDir, "Viewport 服务注册目录")
	appendContextDir(lines, "pan_dir", paths.PanDir, panDirDesc)
}

func appendLocalContextPaths(lines *[]string, paths LocalPaths) {
	appendContextDir(lines, "workspace_dir", paths.WorkspaceDir, "工具默认工作目录 / 权限工作根")
	appendContextDir(lines, "chat_dir", paths.ChatAttachmentsDir, "当前会话文件目录，可存放产物、临时代码和临时文件")
	appendContextDir(lines, "root_dir", paths.RootDir, "root 目录")
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "当前 agent 私有技能目录")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "当前 agent 定义目录")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "owner 用户档案目录")
	appendContextDir(lines, "skills_market_dir", paths.SkillsMarketDir, "共享技能市场目录")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "全部 agent 定义目录")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "团队配置目录")
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "计划任务配置目录")
	appendContextDir(lines, "chats_dir", paths.ChatsDir, "会话记录目录")
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "记忆存储目录")
	appendContextDir(lines, "models_dir", paths.ModelsDir, "模型注册配置目录")
	appendContextDir(lines, "providers_dir", paths.ProvidersDir, "供应商注册配置目录")
	appendContextDir(lines, "mcp_servers_dir", paths.MCPServersDir, "MCP 服务注册目录")
	appendContextDir(lines, "viewport_servers_dir", paths.ViewportServersDir, "Viewport 服务注册目录")
	appendContextDir(lines, "pan_dir", paths.PanDir, "用户网盘目录")
}

// appendContextDir adds a dir entry only if mounted (non-empty), with a description.
func appendContextDir(lines *[]string, key, value, desc string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	*lines = append(*lines, key+": "+strings.TrimSpace(value)+" # "+desc)
}

func buildOwnerSection(paths LocalPaths) string {
	ownerDir := strings.TrimSpace(paths.OwnerDir)
	if ownerDir == "" {
		return ""
	}
	entries := collectOwnerMarkdownFiles(ownerDir)
	if len(entries) == 0 {
		return ""
	}
	lines := []string{"Runtime Context: Owner"}
	for _, file := range entries {
		relative, err := filepath.Rel(ownerDir, file)
		if err != nil {
			continue
		}
		lines = append(lines, "--- file: "+filepath.ToSlash(relative))
		data, err := os.ReadFile(file)
		if err != nil {
			lines = append(lines, "[UNREADABLE: "+filepath.ToSlash(relative)+"]")
			continue
		}
		lines = append(lines, strings.TrimRight(string(data), "\n"))
	}
	return strings.Join(lines, "\n")
}

func collectOwnerMarkdownFiles(ownerDir string) []string {
	var files []string
	_ = filepath.WalkDir(ownerDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") {
			files = append(files, path)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		ri, _ := filepath.Rel(ownerDir, files[i])
		rj, _ := filepath.Rel(ownerDir, files[j])
		return filepath.ToSlash(ri) < filepath.ToSlash(rj)
	})
	return files
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

func buildAllAgentsSection(digests []AgentDigest) string {
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
		if projected > allAgentsPromptMaxChars {
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
	builder.WriteString("Runtime Context: All Agents\n")
	builder.WriteString("以下是平台已注册的智能体摘要。如需了解某个智能体的完整配置，可以自行查看 agents 目录下对应的 agent.yml。\n")
	builder.WriteString(strings.Join(blocks, "\n---\n"))
	if included < total {
		builder.WriteString(fmt.Sprintf("\n[TRUNCATED: all-agents exceeds max chars=%d, included=%d/%d]", allAgentsPromptMaxChars, included, total))
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

func buildMemorySection(session QuerySession, req api.QueryRequest) string {
	sections := make([]string, 0, 3)
	if strings.TrimSpace(session.StableMemoryContext) != "" {
		sections = append(sections, strings.TrimSpace(session.StableMemoryContext))
	}
	if strings.TrimSpace(session.SessionMemoryContext) != "" {
		sections = append(sections, strings.TrimSpace(session.SessionMemoryContext))
	}
	if strings.TrimSpace(session.ObservationContext) != "" {
		sections = append(sections, strings.TrimSpace(session.ObservationContext))
	}
	if strings.TrimSpace(session.WorkflowContext) != "" {
		sections = append(sections, strings.TrimSpace(session.WorkflowContext))
	}
	if len(sections) > 0 {
		return strings.Join(sections, "\n\n")
	}
	if strings.TrimSpace(session.MemoryContext) != "" {
		return "Runtime Context: Agent Memory\n" + strings.TrimSpace(session.MemoryContext)
	}
	return ""
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
