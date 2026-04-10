package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
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
		strings.TrimSpace(session.SoulPrompt),
		buildRuntimeContextPrompt(session, req),
		stageInstructionsPrompt,
		strings.TrimSpace(session.MemoryPrompt),
		stageSystemPrompt,
		strings.TrimSpace(session.SkillCatalogPrompt),
		buildToolAppendix(options.ToolDefinitions, appendConfig, options.IncludeAfterCallHints),
	}
	return joinPromptSections(sections...)
}

func effectivePromptAppendConfig(config PromptAppendConfig) PromptAppendConfig {
	defaults := DefaultPromptAppendConfig()
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
	if len(session.ContextTags) == 0 {
		return ""
	}
	var sections []string
	for _, tag := range session.ContextTags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "system":
			appendIfPresent(&sections, buildSystemEnvironmentSection())
		case "context":
			appendIfPresent(&sections, buildContextSection(session, req))
		case "owner":
			appendIfPresent(&sections, buildOwnerSection(session.RuntimeContext.LocalPaths))
		case "auth":
			appendIfPresent(&sections, buildAuthIdentitySection(session.RuntimeContext.AuthIdentity))
		case "sandbox":
			appendIfPresent(&sections, buildSandboxSection(session.RuntimeContext.SandboxContext))
		case "all-agents":
			appendIfPresent(&sections, buildAllAgentsSection(session.RuntimeContext.AgentDigests))
		case "memory":
			appendIfPresent(&sections, buildMemorySection(session, req))
		default:
		}
	}
	return strings.Join(sections, "\n\n")
}

func buildSystemEnvironmentSection() string {
	now := time.Now()
	lines := []string{
		"Runtime Context: System Environment",
		"os: " + runtime.GOOS,
		"arch: " + runtime.GOARCH,
		"go_version: " + runtime.Version(),
		"timezone: " + now.Location().String(),
		"locale: " + resolveLocale(),
		"current_date: " + now.Format("2006-01-02"),
		"current_datetime: " + now.Format(time.RFC3339),
	}
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

func buildContextSection(session QuerySession, req api.QueryRequest) string {
	lines := []string{"Runtime Context: Context"}
	paths := session.RuntimeContext.SandboxPaths
	appendKeyValue(&lines, "sandbox_workspace_dir", paths.WorkspaceDir)
	appendKeyValue(&lines, "sandbox_root_dir", paths.RootDir)
	appendKeyValue(&lines, "sandbox_skills_dir", paths.SkillsDir)
	appendKeyValue(&lines, "sandbox_skills_market_dir", paths.SkillsMarketDir)
	appendKeyValue(&lines, "sandbox_pan_dir", paths.PanDir)
	appendKeyValue(&lines, "sandbox_agent_dir", paths.AgentDir)
	appendKeyValue(&lines, "sandbox_owner_dir", paths.OwnerDir)
	appendKeyValue(&lines, "sandbox_agents_dir", paths.AgentsDir)
	appendKeyValue(&lines, "sandbox_teams_dir", paths.TeamsDir)
	appendKeyValue(&lines, "sandbox_schedules_dir", paths.SchedulesDir)
	appendKeyValue(&lines, "sandbox_chats_dir", paths.ChatsDir)
	appendKeyValue(&lines, "sandbox_memory_dir", paths.MemoryDir)
	appendKeyValue(&lines, "sandbox_models_dir", paths.ModelsDir)
	appendKeyValue(&lines, "sandbox_providers_dir", paths.ProvidersDir)
	appendKeyValue(&lines, "sandbox_mcp_servers_dir", paths.MCPServersDir)
	appendKeyValue(&lines, "sandbox_viewport_servers_dir", paths.ViewportServersDir)
	appendKeyValue(&lines, "sandbox_tools_dir", paths.ToolsDir)
	appendKeyValue(&lines, "sandbox_viewports_dir", paths.ViewportsDir)
	appendKeyValue(&lines, "chatId", session.ChatID)
	appendKeyValue(&lines, "requestId", session.RequestID)
	appendKeyValue(&lines, "runId", session.RunID)
	appendKeyValue(&lines, "agentKey", session.RuntimeContext.AgentKey)
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
	appendKeyValue(&lines, "configuredEnvironmentId", context.ConfiguredEnvironmentID)
	appendKeyValue(&lines, "defaultEnvironmentId", context.DefaultEnvironmentID)
	appendKeyValue(&lines, "level", context.Level)
	lines = append(lines, fmt.Sprintf("container_hub_enabled: %t", context.ContainerHubEnabled))
	lines = append(lines, fmt.Sprintf("uses_sandbox_bash: %t", context.UsesSandboxBash))
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
	appendKeyValue(&lines, "mode", digest.Mode)
	appendKeyValue(&lines, "modelKey", digest.ModelKey)
	appendInlineList(&lines, "tools", digest.Tools)
	appendInlineList(&lines, "skills", digest.Skills)
	if digest.Sandbox != nil && (strings.TrimSpace(digest.Sandbox.EnvironmentID) != "" || strings.TrimSpace(digest.Sandbox.Level) != "") {
		lines = append(lines, "sandbox:")
		appendIndentedKeyValue(&lines, "environmentId", digest.Sandbox.EnvironmentID)
		appendIndentedKeyValue(&lines, "level", digest.Sandbox.Level)
	}
	return strings.Join(lines, "\n")
}

func buildMemorySection(session QuerySession, req api.QueryRequest) string {
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

func buildToolAppendix(definitions []api.ToolDetailResponse, appendConfig PromptAppendConfig, includeAfterCallHints bool) string {
	if len(definitions) == 0 {
		return ""
	}
	appendConfig = effectivePromptAppendConfig(appendConfig)
	sortedDefs := append([]api.ToolDetailResponse(nil), definitions...)
	sort.Slice(sortedDefs, func(i, j int) bool {
		return normalizePromptToolName(sortedDefs[i]) < normalizePromptToolName(sortedDefs[j])
	})

	descriptionLines := make([]string, 0, len(sortedDefs))
	afterCallLines := make([]string, 0, len(sortedDefs))
	seenDescriptions := map[string]struct{}{}
	seenAfterHints := map[string]struct{}{}
	for _, tool := range sortedDefs {
		kind, _ := tool.Meta["kind"].(string)
		name := normalizePromptToolName(tool)
		if name == "" {
			continue
		}
		displayName := name
		if normalizedKind := strings.ToLower(strings.TrimSpace(kind)); normalizedKind != "" && normalizedKind != "backend" {
			displayName = name + " [" + normalizedKind + "]"
		}
		if description := strings.TrimSpace(tool.Description); description != "" {
			line := "- " + displayName + ": " + description
			if _, ok := seenDescriptions[line]; !ok {
				seenDescriptions[line] = struct{}{}
				descriptionLines = append(descriptionLines, line)
			}
		}
		if includeAfterCallHints {
			if hint := strings.TrimSpace(tool.AfterCallHint); hint != "" {
				line := "- " + displayName + ": " + hint
				if _, ok := seenAfterHints[line]; !ok {
					seenAfterHints[line] = struct{}{}
					afterCallLines = append(afterCallLines, line)
				}
			}
		}
	}

	var sections []string
	if len(descriptionLines) > 0 {
		sections = append(sections, strings.TrimSpace(appendConfig.Tool.ToolDescriptionTitle)+"\n"+strings.Join(descriptionLines, "\n"))
	}
	if includeAfterCallHints && len(afterCallLines) > 0 {
		sections = append(sections, strings.TrimSpace(appendConfig.Tool.AfterCallHintTitle)+"\n"+strings.Join(afterCallLines, "\n"))
	}
	return strings.Join(sections, "\n\n")
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
