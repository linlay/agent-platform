package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

const allAgentsPromptMaxChars = 12000

type PromptBuildOptions struct {
	Stage                   string
	StageInstructionsPrompt string
	StageSystemPrompt       string
	ToolDefinitions         []api.ToolDetailResponse
	IncludeAfterCallHints   bool
}

func buildSystemPrompt(session QuerySession, req api.QueryRequest, _ string, options PromptBuildOptions) string {
	appendConfig := effectivePromptAppendConfig(session.PromptAppend)
	stageInstructionsPrompt := strings.TrimSpace(options.StageInstructionsPrompt)
	if stageInstructionsPrompt == "" {
		stageInstructionsPrompt = resolveStageInstructionsPrompt(session, options.Stage)
	}
	stageSystemPrompt := strings.TrimSpace(options.StageSystemPrompt)
	if stageSystemPrompt == "" {
		stageSystemPrompt = resolveStageSystemPrompt(session, options.Stage)
	}

	sections := []string{
		buildAgentIdentitySection(session),
		strings.TrimSpace(session.SoulPrompt),
		strings.TrimSpace(firstNonBlank(session.StaticMemoryPrompt, session.MemoryPrompt)),
		buildRuntimeContextPrompt(session, req),
		stageInstructionsPrompt,
		stageSystemPrompt,
		strings.TrimSpace(session.SkillCatalogPrompt),
		buildToolAppendix(options.ToolDefinitions, appendConfig, options.IncludeAfterCallHints),
	}
	return joinPromptSections(sections...)
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
	return strings.TrimSpace(session.AgentsPrompt)
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

func buildRuntimeContextPrompt(session QuerySession, req api.QueryRequest) string {
	var sections []string
	for _, tag := range session.ContextTags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "system":
			appendIfPresent(&sections, buildSystemEnvironmentSection(session))
		case "context":
			appendIfPresent(&sections, buildSessionContextSection(session, req))
		case "owner":
			appendIfPresent(&sections, buildOwnerSection(session.RuntimeContext.LocalPaths))
		case "auth":
			appendIfPresent(&sections, buildAuthIdentitySection(session.RuntimeContext.AuthIdentity))
		case "all-agents":
			appendIfPresent(&sections, buildAllAgentsSection(session.RuntimeContext.AgentDigests))
		case "memory":
			appendIfPresent(&sections, buildMemorySection(session, req))
		default:
		}
	}
	if session.AgentHasSandboxConfig || session.RuntimeContext.SandboxContext != nil {
		appendIfPresent(&sections, buildSandboxSection(session.RuntimeContext.SandboxContext))
	}
	return strings.Join(sections, "\n\n")
}

func buildSystemEnvironmentSection(session QuerySession) string {
	now := time.Now()
	tz := now.Location().String()
	if tz == "Local" {
		// Try to resolve a meaningful timezone name instead of "Local"
		zone, _ := now.Zone()
		if zone != "" {
			tz = zone
		}
	}
	lines := []string{
		"Runtime Context: System Environment",
		"os: " + runtime.GOOS,
		"arch: " + runtime.GOARCH,
		"timezone: " + tz,
		"datetime: " + now.Format(time.RFC3339),
		"language: 中文",
	}
	appendContextPaths(&lines, session)
	return strings.Join(lines, "\n")
}

func resolveLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		value = strings.Split(value, ".")[0]
		value = strings.ReplaceAll(value, "_", "-")
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func buildSessionContextSection(session QuerySession, req api.QueryRequest) string {
	lines := []string{"Runtime Context: Session Context"}
	// chatId / runId / requestId first
	appendKeyValue(&lines, "chatId", session.ChatID)
	appendKeyValue(&lines, "runId", session.RunID)
	appendKeyValue(&lines, "requestId", session.RequestID)
	appendKeyValue(&lines, "teamId", session.RuntimeContext.TeamID)

	if summary := summarizeScene(session.RuntimeContext.Scene); summary != "" {
		lines = append(lines, "scene: "+summary)
	}
	appendReferences(&lines, session.RuntimeContext.References)
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func appendContextPaths(lines *[]string, session QuerySession) {
	if session.AgentHasSandboxConfig || session.RuntimeContext.SandboxContext != nil {
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
	appendContextDir(lines, "workspace_dir", paths.WorkspaceDir, "当前工作目录")
	appendContextDir(lines, "root_dir", paths.RootDir, rootDirDesc)
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "当前 agent 私有技能目录")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "当前 agent 定义目录")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "owner 用户档案目录")
	appendContextDir(lines, "skills_market_dir", paths.SkillsMarketDir, "共享技能市场目录")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "全部 agent 定义目录")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "团队配置目录")
	appendContextDir(lines, "schedules_dir", paths.SchedulesDir, "计划任务配置目录")
	appendContextDir(lines, "chats_dir", paths.ChatsDir, "会话记录目录")
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "记忆存储目录")
	appendContextDir(lines, "models_dir", paths.ModelsDir, "模型注册配置目录")
	appendContextDir(lines, "providers_dir", paths.ProvidersDir, "供应商注册配置目录")
	appendContextDir(lines, "mcp_servers_dir", paths.MCPServersDir, "MCP 服务注册目录")
	appendContextDir(lines, "viewport_servers_dir", paths.ViewportServersDir, "Viewport 服务注册目录")
	appendContextDir(lines, "pan_dir", paths.PanDir, panDirDesc)
}

func appendLocalContextPaths(lines *[]string, paths LocalPaths) {
	workspaceDir := firstNonBlank(paths.ChatAttachmentsDir, paths.WorkingDirectory)
	appendContextDir(lines, "workspace_dir", workspaceDir, "当前工作目录")
	appendContextDir(lines, "root_dir", paths.RootDir, "root 目录")
	appendContextDir(lines, "skills_dir", paths.SkillsDir, "当前 agent 私有技能目录")
	appendContextDir(lines, "agent_dir", paths.AgentDir, "当前 agent 定义目录")
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "owner 用户档案目录")
	appendContextDir(lines, "skills_market_dir", paths.SkillsMarketDir, "共享技能市场目录")
	appendContextDir(lines, "agents_dir", paths.AgentsDir, "全部 agent 定义目录")
	appendContextDir(lines, "teams_dir", paths.TeamsDir, "团队配置目录")
	appendContextDir(lines, "schedules_dir", paths.SchedulesDir, "计划任务配置目录")
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

func buildAuthIdentitySection(identity *AuthIdentity) string {
	if identity == nil {
		return ""
	}
	lines := []string{"Runtime Context: Auth Identity"}
	appendKeyValue(&lines, "subject", identity.Subject)
	appendKeyValue(&lines, "deviceId", identity.DeviceID)
	appendKeyValue(&lines, "scope", identity.Scope)
	appendKeyValue(&lines, "issuedAt", identity.IssuedAt)
	appendKeyValue(&lines, "expiresAt", identity.ExpiresAt)
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
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
		lines = append(lines, "extraMounts:")
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
	if memoryText, _ := req.Params["memoryContext"].(string); strings.TrimSpace(memoryText) != "" {
		return "Runtime Context: Agent Memory\n" + strings.TrimSpace(memoryText)
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

func appendReferences(lines *[]string, references []api.Reference) {
	if len(references) == 0 {
		return
	}
	*lines = append(*lines, "references:")
	for _, reference := range references {
		fields := make([]string, 0, 5)
		appendReferenceField(&fields, "id", reference.ID)
		appendReferenceField(&fields, "sandboxPath", reference.SandboxPath)
		appendReferenceField(&fields, "name", reference.Name)
		if reference.SizeBytes != nil {
			fields = append(fields, fmt.Sprintf("sizeBytes: %d", *reference.SizeBytes))
		}
		appendReferenceField(&fields, "mimeType", reference.MimeType)
		if len(fields) == 0 {
			continue
		}
		*lines = append(*lines, "  - "+fields[0])
		for _, field := range fields[1:] {
			*lines = append(*lines, "    "+field)
		}
	}
}

func appendReferenceField(fields *[]string, key string, value string) {
	if strings.TrimSpace(value) != "" {
		*fields = append(*fields, key+": "+sanitizeYAMLScalar(strings.TrimSpace(value)))
	}
}

func sanitizeYAMLScalar(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return value
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

func appendIfPresent(sections *[]string, content string) {
	if strings.TrimSpace(content) != "" {
		*sections = append(*sections, strings.TrimSpace(content))
	}
}

func appendKeyValue(lines *[]string, key string, value string) {
	if strings.TrimSpace(value) != "" {
		*lines = append(*lines, key+": "+strings.TrimSpace(value))
	}
}

func appendIndentedKeyValue(lines *[]string, key string, value string) {
	if strings.TrimSpace(value) != "" {
		*lines = append(*lines, "  "+key+": "+strings.TrimSpace(value))
	}
}

func appendInlineList(lines *[]string, key string, values []string) {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			normalized = append(normalized, strings.TrimSpace(value))
		}
	}
	if len(normalized) > 0 {
		*lines = append(*lines, key+": ["+strings.Join(normalized, ", ")+"]")
	}
}
