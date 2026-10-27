package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/agent/planmode"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
)

type PromptBuildOptions struct {
	Stage                 string
	StageSystemPrompt     string
	ToolDefinitions       []api.ToolDetailResponse
	IncludeAfterCallHints bool
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
	appendSection("reference-protocol", "Reference Context Protocol", "references.protocol", appendConfig.Reference.ProtocolPrompt)
	if session.AdvancedUserPrompt {
		appendSection("advanced-user-prompt-protocol", "Advanced User Prompt Protocol", "query.advanced_user_prompt", appendConfig.Reference.AdvancedProtocolPrompt)
	}
	appendRuntimeSystemPromptSections(&sections, session)
	appendSection("runtime-path-policy", "Runtime Context: Path Policy", "runtime.path_policy", buildRuntimePathPolicySection(session, options.ToolDefinitions))
	appendSection("runtime-plan-tasks", "Runtime Context: Current Plan Tasks", "runtime.plan_tasks", buildPlanTaskContextSection(session, toolNames, options.Stage))
	appendSection("stage-system", "Stage System Prompt", "stage.system", stageSystemPrompt)
	appendSection("tool-announce", "Progress Announcements", "tools.announce", buildAnnounceSection(options.ToolDefinitions, appendConfig))
	appendSection("skill-catalog", "Skill Catalog Prompt", "skills.catalog", strings.TrimSpace(session.SkillCatalogPrompt))
	appendSection("view-catalog", "View Catalog Prompt", "views.catalog", strings.TrimSpace(session.ViewCatalogPrompt))
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
		case "memory-global":
			appendSection("runtime-memory-global", "Runtime Context: Global Memory", "memory.global", session.GlobalMemoryContext)
		case "memory-agent":
			appendSection("runtime-memory-agent", "Runtime Context: Agent Memory", "memory.agent", session.AgentMemoryContext)
		}
	}
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSection("runtime-sandbox", "Runtime Context: Sandbox", "runtime.sandbox", buildSandboxSection(session.RuntimeContext.SandboxContext))
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
	if !hasPathTools {
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
		lines = append(lines, "- File paths must use an explicit semantic root such as @chat, @agent, @skills, @skills-center, @connectors, @owner, @runtime, or @temp, or an allowed absolute path. Relative paths and @workspace fail with workspace_unavailable.")
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

// localTimezoneName returns an IANA timezone name or the current UTC offset.
// "Local" uses TZ or the /etc/localtime zoneinfo link to recover its name.
func localTimezoneName() string {
	tz := time.Local.String()
	if zone := normalizeTimezoneName(tz); zone != "" {
		return zone
	}
	if tz == "Local" {
		if zone := normalizeTimezoneName(os.Getenv("TZ")); zone != "" {
			return zone
		}
		if target, err := os.Readlink("/etc/localtime"); err == nil {
			if zone := normalizeTimezoneName(target); zone != "" {
				return zone
			}
		}
	}
	return "UTC" + time.Now().Format("-07:00")
}

func normalizeTimezoneName(value string) string {
	zone := strings.TrimPrefix(strings.TrimSpace(value), ":")
	if _, name, ok := strings.Cut(filepath.ToSlash(zone), "/zoneinfo/"); ok {
		zone = name
	}
	if zone == "" || zone == "Local" {
		return ""
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return ""
	}
	return zone
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
	defaults.Skill.InstructionsPrompt = strings.TrimSpace(config.Skill.InstructionsPrompt)
	defaults.Reference.ProtocolPrompt = strings.TrimSpace(config.Reference.ProtocolPrompt)
	defaults.Reference.AdvancedProtocolPrompt = strings.TrimSpace(config.Reference.AdvancedProtocolPrompt)
	defaults.Announce.InstructionsPrompt = strings.TrimSpace(config.Announce.InstructionsPrompt)
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

func resolveStageSystemPrompt(session QuerySession, stage string) string {
	if planmode.IsPlanningStage(stage) && agentbuiltin.PlanningModeSupported(session.Mode) {
		return strings.TrimSpace(session.ResolvedPlanningSettings.Planning.SystemPrompt)
	}
	if strings.EqualFold(strings.TrimSpace(stage), "summary") {
		return strings.TrimSpace(session.ResolvedPlanningSettings.Summary.SystemPrompt)
	}
	return strings.TrimSpace(session.ResolvedPlanningSettings.Execute.SystemPrompt)
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

// chatDirDescription tells the model where run-authored scripts skip execution
// approval; the rule itself lives in accesspolicy.authoredScriptExempt.
const chatDirDescription = "Current chat files, artifacts, temporary code and files; scripts written here with file_write run without approval"

func appendContextPaths(lines *[]string, session QuerySession) {
	// Platform source directories only matter to Agents that can manage them.
	platform := session.ConnectorDirs[connector.PlatformControlConnectorID] != ""
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSandboxContextPaths(lines, session.RuntimeContext.SandboxPaths, session.RuntimeContext.LocalMode, platform)
		return
	}
	appendLocalContextPaths(lines, session.RuntimeContext.LocalPaths, platform)
}

func appendSandboxContextPaths(lines *[]string, paths SandboxPaths, localMode bool, platform bool) {
	rootDirDesc := "Container home directory"
	if localMode {
		rootDirDesc = ""
	}
	appendSemanticRoot(lines, "workspace_dir", paths.WorkspaceDir, "Relative path base / permission workspace root")
	appendSemanticRoot(lines, "chat_dir", paths.ChatDir, chatDirDescription)
	appendContextDir(lines, "root_dir", paths.RootDir, rootDirDesc)
	appendAgentContextDirs(lines, paths.AgentDir, paths.SkillsDir, paths.RUAgentsDir, paths.AgentDir, paths.SkillsDir, paths.RUAgentsDir)
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "")
	appendContextDir(lines, "skills_center_dir", paths.SkillsCenterDir, "")
	if platform {
		appendContextDir(lines, "agents_dir", paths.AgentsDir, "Editable Agent sources")
	}
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "")
	if !pathWithin(paths.ChatsDir, paths.ChatDir) {
		appendContextDir(lines, "chats_dir", paths.ChatsDir, "")
	}
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "")
	if platform {
		appendContextDir(lines, "models_dir", paths.ModelsDir, "")
		appendContextDir(lines, "providers_dir", paths.ProvidersDir, "")
		appendContextDir(lines, "connectors_center_dir", paths.ConnectorsCenterDir, "Editable connector package sources")
	}
	appendContextDir(lines, "connectors_dir", paths.ConnectorsDir, connectorsDirDescription)
	appendContextDir(lines, "pan_dir", paths.PanDir, "")
}

// appendLocalContextPaths lists the runtime root once as an absolute path and
// shows directories under it as @runtime paths, which the tools resolve.
func appendLocalContextPaths(lines *[]string, paths LocalPaths, platform bool) {
	local := paths
	paths = runtimeQualifiedLocalPaths(paths)
	appendContextDir(lines, "runtime_dir", local.RuntimeHome, "@runtime resolves here; use the absolute path inside shell commands")
	appendSemanticRoot(lines, "workspace_dir", paths.WorkspaceDir, "Relative path base / permission workspace root")
	appendSemanticRoot(lines, "chat_dir", paths.ChatDir, chatDirDescription)
	appendContextDir(lines, "root_dir", paths.RootDir, "")
	appendAgentContextDirs(lines, paths.AgentDir, paths.SkillsDir, paths.RUAgentsDir, local.AgentDir, local.SkillsDir, local.RUAgentsDir)
	appendContextDir(lines, "owner_dir", paths.OwnerDir, "")
	appendContextDir(lines, "skills_center_dir", paths.SkillsCenterDir, "")
	if platform {
		appendContextDir(lines, "agents_dir", paths.AgentsDir, "Editable Agent sources")
	}
	appendContextDir(lines, "automations_dir", paths.AutomationsDir, "")
	if !pathWithin(local.ChatsDir, local.ChatDir) {
		appendContextDir(lines, "chats_dir", paths.ChatsDir, "")
	}
	appendContextDir(lines, "memory_dir", paths.MemoryDir, "")
	if platform {
		appendContextDir(lines, "models_dir", paths.ModelsDir, "")
		appendContextDir(lines, "providers_dir", paths.ProvidersDir, "")
		appendContextDir(lines, "connectors_center_dir", paths.ConnectorsCenterDir, "Editable connector package sources")
	}
	appendContextDir(lines, "connectors_dir", paths.ConnectorsDir, connectorsDirDescription)
	appendContextDir(lines, "pan_dir", paths.PanDir, "")
}

const connectorsDirDescription = "Mounted read-only connector packages; use full paths listed in the skill catalog"

// appendAgentContextDirs folds skills_dir and ru_agents_dir into the agent_dir
// line when they are its skills/ child and its ancestor; otherwise each keeps
// its own line. The shown values may be @runtime-qualified, the raw ones decide
// containment.
func appendAgentContextDirs(lines *[]string, agentDir, skillsDir, ruAgentsDir, rawAgentDir, rawSkillsDir, rawRUAgentsDir string) {
	generated := pathWithin(rawRUAgentsDir, rawAgentDir)
	skillsInside := strings.TrimSpace(rawAgentDir) != "" && filepath.Clean(rawSkillsDir) == filepath.Join(filepath.Clean(rawAgentDir), "skills")
	if !skillsInside {
		appendContextDir(lines, "skills_dir", skillsDir, "Current Agent private skills")
	}
	desc := "Current Agent runtime directory"
	if generated {
		desc += "; platform-generated, do not edit"
	}
	if skillsInside {
		desc += "; private skills in skills/"
	}
	appendContextDir(lines, "agent_dir", agentDir, desc)
	if !generated {
		appendContextDir(lines, "ru_agents_dir", ruAgentsDir, "Platform-generated Agent execution directory; do not edit manually")
	}
}

// pathWithin reports whether child is strictly under parent.
func pathWithin(parent, child string) bool {
	parent, child = strings.TrimSpace(parent), strings.TrimSpace(child)
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func runtimeQualifiedLocalPaths(paths LocalPaths) LocalPaths {
	home := strings.TrimSpace(paths.RuntimeHome)
	if home == "" {
		return paths
	}
	for _, value := range []*string{
		&paths.AgentsDir, &paths.RUAgentsDir, &paths.AutomationsDir, &paths.ChatsDir,
		&paths.ChatDir, &paths.SkillsDir, &paths.AgentDir, &paths.OwnerDir, &paths.SkillsCenterDir,
		&paths.MemoryDir, &paths.ModelsDir, &paths.ProvidersDir, &paths.ConnectorsCenterDir,
		&paths.ConnectorsDir, &paths.PanDir,
	} {
		*value = runtimeQualifiedPath(home, *value)
	}
	return paths
}

// runtimeQualifiedPath rewrites a directory strictly under home; anything else
// (including the Workspace and home itself) keeps its absolute form.
func runtimeQualifiedPath(home, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if !pathWithin(home, value) {
		return value
	}
	rel, _ := filepath.Rel(filepath.Clean(home), filepath.Clean(value))
	return "@runtime/" + filepath.ToSlash(rel)
}

func appendSemanticRoot(lines *[]string, key, value, desc string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "unavailable"
	}
	*lines = append(*lines, key+": "+value+" # "+desc)
}

// appendContextDir adds a dir entry only if mounted (non-empty). The
// description is optional: a key that already names its directory gets none.
func appendContextDir(lines *[]string, key, value, desc string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	line := key + ": " + strings.TrimSpace(value)
	if desc != "" {
		line += " # " + desc
	}
	*lines = append(*lines, line)
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

// buildAnnounceSection renders the announcement rules when the stage has a
// tool whose activityAnnounce turns its call into the Run's progress line.
func buildAnnounceSection(definitions []api.ToolDetailResponse, appendConfig PromptAppendConfig) string {
	prompt := strings.TrimSpace(appendConfig.Announce.InstructionsPrompt)
	if prompt == "" {
		return ""
	}
	names := make([]string, 0, 1)
	for _, tool := range definitions {
		if ToolActivityAnnounce(tool.Meta) == "" {
			continue
		}
		if name := normalizePromptToolName(tool); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return agentcontract.RenderPromptTemplate(prompt, map[string]string{"announce_tool_name": names[0]})
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
