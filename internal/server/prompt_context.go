package server

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
	"agent-platform/internal/sandbox"
)

func buildPromptAppendConfig(global config.PromptsConfig, def catalog.AgentDefinition) contracts.PromptAppendConfig {
	config := contracts.DefaultPromptAppendConfig()
	if strings.TrimSpace(global.Skill.InstructionsPrompt) != "" {
		config.Skill.InstructionsPrompt = strings.TrimSpace(global.Skill.InstructionsPrompt)
	}
	if strings.TrimSpace(global.Skill.CatalogHeader) != "" {
		config.Skill.CatalogHeader = strings.TrimSpace(global.Skill.CatalogHeader)
	}
	if strings.TrimSpace(global.Skill.DisclosureHeader) != "" {
		config.Skill.DisclosureHeader = strings.TrimSpace(global.Skill.DisclosureHeader)
	}
	if strings.TrimSpace(global.Skill.InstructionsLabel) != "" {
		config.Skill.InstructionsLabel = strings.TrimSpace(global.Skill.InstructionsLabel)
	}
	if strings.TrimSpace(global.ToolAppendix.ToolDescriptionTitle) != "" {
		config.Tool.ToolDescriptionTitle = strings.TrimSpace(global.ToolAppendix.ToolDescriptionTitle)
	}
	if strings.TrimSpace(global.ToolAppendix.AfterCallHintTitle) != "" {
		config.Tool.AfterCallHintTitle = strings.TrimSpace(global.ToolAppendix.AfterCallHintTitle)
	}
	if strings.TrimSpace(def.RuntimePrompts.Skill.CatalogHeader) != "" {
		config.Skill.CatalogHeader = strings.TrimSpace(def.RuntimePrompts.Skill.CatalogHeader)
	}
	if strings.TrimSpace(def.RuntimePrompts.Skill.DisclosureHeader) != "" {
		config.Skill.DisclosureHeader = strings.TrimSpace(def.RuntimePrompts.Skill.DisclosureHeader)
	}
	if strings.TrimSpace(def.RuntimePrompts.Skill.InstructionsLabel) != "" {
		config.Skill.InstructionsLabel = strings.TrimSpace(def.RuntimePrompts.Skill.InstructionsLabel)
	}
	if strings.TrimSpace(def.RuntimePrompts.ToolAppendix.ToolDescriptionTitle) != "" {
		config.Tool.ToolDescriptionTitle = strings.TrimSpace(def.RuntimePrompts.ToolAppendix.ToolDescriptionTitle)
	}
	if strings.TrimSpace(def.RuntimePrompts.ToolAppendix.AfterCallHintTitle) != "" {
		config.Tool.AfterCallHintTitle = strings.TrimSpace(def.RuntimePrompts.ToolAppendix.AfterCallHintTitle)
	}
	return config
}

type runtimeRequestContextInput struct {
	agentKey   string
	teamID     string
	role       string
	chatID     string
	chatName   string
	scene      *api.Scene
	references []api.Reference
	principal  *Principal
	definition catalog.AgentDefinition
}

func (s *Server) buildRuntimeRequestContext(input runtimeRequestContextInput) (contracts.RuntimeRequestContext, error) {
	workspaceRoot := effectiveLocalWorkspaceRoot(input.definition)
	if strings.EqualFold(catalog.NormalizeAgentModeForRuntime(input.definition.Mode), catalog.AgentModeCoder) &&
		strings.TrimSpace(workspaceRoot) == "" {
		return contracts.RuntimeRequestContext{}, fmt.Errorf("workspace_unavailable: CODER requires a workspace")
	}
	if hasRuntimeSandbox(input.definition.Runtime) && strings.TrimSpace(workspaceRoot) == "" {
		return contracts.RuntimeRequestContext{}, fmt.Errorf("workspace_unavailable: Container Hub sandbox requires a workspace")
	}
	localPaths, err := resolveLocalPaths(s.deps.Config.Paths, input.chatID, input.definition.AgentDir, workspaceRoot)
	if err != nil {
		return contracts.RuntimeRequestContext{}, err
	}
	if promptContextHasPlatformMount(input.definition.Runtime["sandboxMounts"], "skills-market") {
		localPaths.SkillsMarketDir = cleanOrEmpty(s.deps.Config.Paths.SkillsMarketDir)
	}
	references, err := s.normalizeReferencePathsForAgent(input.references, input.chatID, input.definition, localPaths)
	if err != nil {
		return contracts.RuntimeRequestContext{}, err
	}
	context := contracts.RuntimeRequestContext{
		AgentKey:     input.agentKey,
		TeamID:       input.teamID,
		Role:         input.role,
		ChatName:     input.chatName,
		LocalMode:    s.deps.Config.IsLocalMode(),
		Scene:        input.scene,
		References:   references,
		LocalPaths:   localPaths,
		SandboxPaths: resolveSandboxPaths(s.deps.Config, input.definition, localPaths),
	}
	agentDigests, err := buildContextAgentDigests(s.deps.Registry, input.definition)
	if err != nil {
		return contracts.RuntimeRequestContext{}, err
	}
	context.AgentDigests = agentDigests
	if input.principal != nil {
		context.AuthIdentity = buildAuthIdentity(input.principal)
	}
	if hasRuntimeSandbox(input.definition.Runtime) && s.deps.Config.ContainerHub.Enabled {
		sandboxContext, err := buildSandboxContext(s.deps.Config, input.definition)
		if err != nil {
			return contracts.RuntimeRequestContext{}, err
		}
		context.SandboxContext = sandboxContext
	}
	return context, nil
}

func (s *Server) normalizeReferencePathsForAgent(
	references []api.Reference,
	chatID string,
	def catalog.AgentDefinition,
	localPaths contracts.LocalPaths,
) ([]api.Reference, error) {
	if len(references) == 0 {
		return references, nil
	}
	normalized := append([]api.Reference(nil), references...)
	for i := range normalized {
		path, err := s.referencePathForAgent(normalized[i], chatID, def, localPaths)
		if err != nil {
			return nil, err
		}
		if path != "" {
			normalized[i].Path = path
		}
	}
	return normalized, nil
}

func (s *Server) referencePathForAgent(
	reference api.Reference,
	chatID string,
	def catalog.AgentDefinition,
	localPaths contracts.LocalPaths,
) (string, error) {
	switch strings.ToLower(strings.TrimSpace(reference.Type)) {
	case "chat", "site":
		return "", nil
	}
	if resourceFileParam(reference.URL) == "" {
		rawPath := strings.TrimSpace(reference.Path)
		if rawPath == "/workspace" || strings.HasPrefix(rawPath, "/workspace/") {
			return "", fmt.Errorf("path-only /workspace references are not accepted; re-materialize the file through the resource API")
		}
	}
	if s.agentUsesContainerHub(def) {
		if fileParam := resourceFileParam(reference.URL); fileParam != "" {
			rel, ok := currentChatResourceRelativePath(chatID, fileParam)
			if !ok {
				return "", fmt.Errorf("reference resource must be materialized in the current chat before Container Hub execution")
			}
			return "/chat/" + filepath.ToSlash(rel), nil
		}
		return translateReferencePathForContainer(reference.Path, localPaths)
	}
	if fileParam := resourceFileParam(reference.URL); fileParam != "" && s != nil && s.deps.Chats != nil {
		if path, err := s.deps.Chats.ResolveResource(fileParam); err == nil {
			return path, nil
		}
	}
	if strings.TrimSpace(reference.Path) != "" {
		return translateReferencePathForHost(reference.Path, localPaths)
	}
	if rel := referenceResourceRelativePath(chatID, reference); rel != "" && s != nil && s.deps.Chats != nil {
		return filepath.Join(s.deps.Chats.ChatDir(chatID), filepath.FromSlash(rel)), nil
	}
	return "", nil
}

func translateReferencePathForHost(rawPath string, localPaths contracts.LocalPaths) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", nil
	}
	for _, item := range []struct {
		alias string
		root  string
	}{
		{alias: "@chat", root: localPaths.ChatDir},
		{alias: "@workspace", root: localPaths.WorkspaceDir},
	} {
		normalized := filepath.ToSlash(rawPath)
		if strings.EqualFold(normalized, item.alias) {
			if strings.TrimSpace(item.root) == "" {
				return "", fmt.Errorf("%s_unavailable: reference root is unavailable", strings.TrimPrefix(item.alias, "@"))
			}
			resolved := filepath.Clean(item.root)
			if item.alias == "@workspace" {
				return requireReferenceWorkspacePath(resolved, localPaths)
			}
			return resolved, nil
		}
		prefix := item.alias + "/"
		if strings.HasPrefix(strings.ToLower(normalized), prefix) {
			if strings.TrimSpace(item.root) == "" {
				return "", fmt.Errorf("%s_unavailable: reference root is unavailable", strings.TrimPrefix(item.alias, "@"))
			}
			resolved, err := referencePathWithinHostRoot(item.root, normalized[len(prefix):], item.alias)
			if err != nil || item.alias != "@workspace" {
				return resolved, err
			}
			return requireReferenceWorkspacePath(resolved, localPaths)
		}
	}
	if rawPath == "/chat" || strings.HasPrefix(rawPath, "/chat/") {
		return referencePathWithinHostRoot(localPaths.ChatDir, strings.TrimLeft(strings.TrimPrefix(rawPath, "/chat"), "/"), "@chat")
	}
	if !filepath.IsAbs(pathutil.ExpandHome(rawPath)) {
		if strings.TrimSpace(localPaths.WorkspaceDir) == "" {
			return "", fmt.Errorf("workspace_unavailable: relative reference path requires a workspace")
		}
		resolved, err := referencePathWithinHostRoot(localPaths.WorkspaceDir, rawPath, "@workspace")
		if err != nil {
			return "", err
		}
		return requireReferenceWorkspacePath(resolved, localPaths)
	}
	roots, err := localSemanticRoots(localPaths)
	if err != nil {
		return "", err
	}
	zone, candidate, err := roots.Classify(rawPath)
	if err != nil {
		return "", err
	}
	switch zone {
	case rootpaths.ZoneCurrentChat, rootpaths.ZoneWorkspace:
		return candidate.Host, nil
	case rootpaths.ZoneOtherChat:
		return "", fmt.Errorf("reference path belongs to another chat")
	default:
		return "", fmt.Errorf("reference path must be under the current workspace or chat")
	}
}

func referencePathWithinHostRoot(root string, suffix string, alias string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("%s_unavailable: reference root is unavailable", strings.TrimPrefix(alias, "@"))
	}
	resolved := filepath.Clean(filepath.Join(root, filepath.FromSlash(suffix)))
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference path escapes %s", alias)
	}
	canonical, err := pathutil.Canonicalize(resolved)
	if err != nil {
		return "", err
	}
	rootCanonical, err := pathutil.Canonicalize(root)
	if err != nil || !pathutil.WithinRoot(canonical, rootCanonical) {
		return "", fmt.Errorf("reference path escapes %s", alias)
	}
	return canonical.Host, nil
}

func translateReferencePathForContainer(rawPath string, localPaths contracts.LocalPaths) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", nil
	}
	if rawPath == "/chat" || strings.HasPrefix(rawPath, "/chat/") {
		return filepath.ToSlash(rawPath), nil
	}
	if rawPath == "/workspace" || strings.HasPrefix(rawPath, "/workspace/") {
		return "", fmt.Errorf("path-only /workspace references are not accepted; use a resource URL or @workspace")
	}
	hostPath, err := translateReferencePathForHost(rawPath, localPaths)
	if err != nil {
		return "", err
	}
	roots, err := localSemanticRoots(localPaths)
	if err != nil {
		return "", err
	}
	zone, candidate, err := roots.Classify(hostPath)
	if err != nil {
		return "", err
	}
	var hostRoot string
	var containerRoot string
	switch zone {
	case rootpaths.ZoneCurrentChat:
		hostRoot = roots.Chat.Host
		containerRoot = "/chat"
	case rootpaths.ZoneWorkspace:
		hostRoot = roots.Workspace.Host
		containerRoot = "/workspace"
	default:
		return "", fmt.Errorf("container reference path must be under the current workspace or chat")
	}
	rel, err := filepath.Rel(hostRoot, candidate.Host)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return containerRoot, nil
	}
	return containerRoot + "/" + filepath.ToSlash(rel), nil
}

func localSemanticRoots(localPaths contracts.LocalPaths) (rootpaths.Roots, error) {
	return rootpaths.New(localPaths.WorkspaceDir, localPaths.ChatsDir, localPaths.ChatDir)
}

func requireReferenceWorkspacePath(candidate string, localPaths contracts.LocalPaths) (string, error) {
	roots, err := localSemanticRoots(localPaths)
	if err != nil {
		return "", err
	}
	resolved, err := roots.RequireWorkspacePath(candidate)
	if err != nil {
		return "", err
	}
	return resolved.Host, nil
}

func currentChatResourceRelativePath(chatID string, fileParam string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(fileParam))
	prefix := strings.TrimSpace(chatID) + "/"
	if strings.TrimSpace(chatID) == "" || !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(clean, prefix)
	return rel, rel != "" && rel != "."
}

func referenceResourceRelativePath(chatID string, reference api.Reference) string {
	if fileParam := resourceFileParam(reference.URL); fileParam != "" {
		clean := filepath.ToSlash(filepath.Clean(fileParam))
		prefix := strings.TrimSpace(chatID) + "/"
		if strings.TrimSpace(chatID) != "" && strings.HasPrefix(clean, prefix) {
			return strings.TrimPrefix(clean, prefix)
		}
		return clean
	}
	if strings.TrimSpace(reference.Path) != "" {
		return ""
	}
	return referenceName(reference)
}

func referenceName(reference api.Reference) string {
	for _, candidate := range []string{
		reference.Name,
		resourceFileName(reference.URL),
	} {
		name := filepath.Base(filepath.ToSlash(strings.TrimSpace(candidate)))
		if name != "" && name != "." && name != "/" {
			return name
		}
	}
	return ""
}

func resourceFileName(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if fileParam := strings.TrimSpace(parsed.Query().Get("file")); fileParam != "" {
		return fileParam
	}
	return parsed.Path
}

func buildSkillCatalogPrompt(def catalog.AgentDefinition, marketDir string, appendConfig contracts.PromptAppendConfig) string {
	if len(def.Skills) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(def.Skills))
	seen := map[string]struct{}{}
	for _, configuredSkill := range def.Skills {
		skillID := strings.ToLower(strings.TrimSpace(configuredSkill))
		if skillID == "" {
			continue
		}
		if _, ok := seen[skillID]; ok {
			continue
		}
		seen[skillID] = struct{}{}
		definition, ok, err := catalog.ResolveSkillDefinition(def.AgentDir, marketDir, skillID)
		if err != nil {
			log.Printf("[server][skill-catalog][warn] resolve skill %s failed: %v", skillID, err)
			continue
		}
		if !ok {
			continue
		}
		lines := []string{"skillId: " + definition.Key}
		if strings.TrimSpace(definition.Name) != "" {
			lines = append(lines, "name: "+strings.TrimSpace(definition.Name))
		}
		if strings.TrimSpace(definition.Description) != "" {
			lines = append(lines, "description: "+strings.TrimSpace(definition.Description))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(blocks) == 0 {
		return ""
	}
	sections := make([]string, 0, 3)
	if instructionsPrompt := strings.TrimSpace(appendConfig.Skill.InstructionsPrompt); instructionsPrompt != "" {
		label := strings.TrimSpace(appendConfig.Skill.InstructionsLabel)
		if label != "" {
			sections = append(sections, "Skill "+label+":\n"+instructionsPrompt)
		} else {
			sections = append(sections, instructionsPrompt)
		}
	}
	sections = append(sections, strings.TrimSpace(appendConfig.Skill.CatalogHeader))
	sections = append(sections, strings.Join(blocks, "\n\n---\n\n"))
	return strings.Join(sections, "\n\n")
}

func resolveRequiredSkillKeys(def catalog.AgentDefinition, marketDir string, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	configured := make(map[string]string, len(def.Skills))
	for _, key := range def.Skills {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized != "" {
			configured[normalized] = strings.TrimSpace(key)
		}
	}
	resolved := make([]string, 0, len(requested))
	seen := map[string]struct{}{}
	for _, key := range requested {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		configuredKey, ok := configured[normalized]
		if !ok {
			return nil, fmt.Errorf("required skill %q is not configured for agent %q", key, def.Key)
		}
		definition, found, err := catalog.ResolveSkillDefinition(def.AgentDir, marketDir, configuredKey)
		if err != nil {
			return nil, fmt.Errorf("resolve required skill %q: %w", configuredKey, err)
		}
		if !found {
			return nil, fmt.Errorf("required skill %q could not be resolved", configuredKey)
		}
		resolved = append(resolved, definition.Key)
	}
	return resolved, nil
}

func buildRequiredSkillConstraint(requiredSkillKeys []string) string {
	if len(requiredSkillKeys) == 0 {
		return ""
	}
	lines := []string{
		"Required skills for this run:",
	}
	for _, key := range requiredSkillKeys {
		if normalized := strings.TrimSpace(key); normalized != "" {
			lines = append(lines, "- "+normalized)
		}
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(
		lines,
		"You must load the complete SKILL.md instructions for every required skill above and follow them for this run. This requirement is mandatory and must not be silently ignored or replaced by another skill.",
	)
	return strings.Join(lines, "\n")
}

func effectiveLocalWorkspaceRoot(def catalog.AgentDefinition) string {
	return strings.TrimSpace(def.Workspace.Root)
}

func resolveLocalPaths(paths config.PathsConfig, chatID string, agentDir string, workspaceRoot string) (contracts.LocalPaths, error) {
	runtimeHome := filepath.Dir(filepath.Clean(paths.AgentsDir))
	var err error
	workspaceRoot, err = resolveHostWorkspaceRoot(workspaceRoot)
	if err != nil {
		return contracts.LocalPaths{}, err
	}
	if err := validateWorkspaceChatsSeparation(workspaceRoot, paths.ChatsDir); err != nil {
		return contracts.LocalPaths{}, err
	}
	chatDir, err := ensureChatDir(paths, chatID)
	if err != nil {
		return contracts.LocalPaths{}, err
	}
	agentDir = cleanOrEmpty(agentDir)
	agentSkillsDir := ""
	if agentDir != "" {
		agentSkillsDir = cleanOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return contracts.LocalPaths{
		RuntimeHome:        runtimeHome,
		WorkspaceDir:       workspaceRoot,
		ChatDir:            chatDir,
		RootDir:            cleanOrEmpty(paths.RootDir),
		PanDir:             cleanOrEmpty(paths.PanDir),
		AgentDir:           agentDir,
		AgentsDir:          cleanOrEmpty(paths.AgentsDir),
		TeamsDir:           cleanOrEmpty(paths.TeamsDir),
		ChatsDir:           cleanOrEmpty(paths.ChatsDir),
		MemoryDir:          cleanOrEmpty(paths.MemoryDir),
		SkillsDir:          agentSkillsDir,
		AutomationsDir:     cleanOrEmpty(paths.AutomationsDir),
		OwnerDir:           cleanOrEmpty(paths.OwnerDir),
		ModelsDir:          cleanOrEmpty(filepath.Join(paths.RegistriesDir, "models")),
		ProvidersDir:       cleanOrEmpty(filepath.Join(paths.RegistriesDir, "providers")),
		MCPServersDir:      cleanOrEmpty(filepath.Join(paths.RegistriesDir, "mcp-servers")),
		ViewportServersDir: cleanOrEmpty(filepath.Join(paths.RegistriesDir, "viewport-servers")),
		ToolsDir:           cleanOrEmpty(paths.ToolsDir),
		ViewportsDir:       cleanOrEmpty(filepath.Join(filepath.Dir(filepath.Clean(paths.RegistriesDir)), "viewports")),
	}, nil
}

func validateWorkspaceChatsSeparation(workspaceRoot string, chatsRoot string) error {
	if strings.TrimSpace(workspaceRoot) == "" || strings.TrimSpace(chatsRoot) == "" {
		return nil
	}
	if _, err := rootpaths.New(workspaceRoot, chatsRoot, ""); err != nil {
		return fmt.Errorf("workspace/chats validation failed: %w", err)
	}
	return nil
}

func resolveHostWorkspaceRoot(workspaceRoot string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", nil
	}
	if strings.EqualFold(workspaceRoot, "@chat") {
		return "", fmt.Errorf("workspaceRoot no longer supports %q", "@chat")
	}
	canonical, err := pathutil.Canonicalize(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory %s: %w", workspaceRoot, err)
	}
	info, err := os.Stat(canonical.Host)
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory %s: %w", workspaceRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory: %s", workspaceRoot)
	}
	return canonical.Host, nil
}

func ensureChatDir(paths config.PathsConfig, chatID string) (string, error) {
	dir := chatDirPath(paths, chatID)
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create chat directory %s: %w", dir, err)
	}
	canonical, err := pathutil.Canonicalize(dir)
	if err != nil {
		return "", fmt.Errorf("resolve chat directory %s: %w", dir, err)
	}
	return canonical.Host, nil
}

func chatDirPath(paths config.PathsConfig, chatID string) string {
	chatID = strings.TrimSpace(chatID)
	chatsDir := strings.TrimSpace(paths.ChatsDir)
	if chatID == "" || chatsDir == "" {
		return ""
	}
	return absOrEmpty(filepath.Join(chatsDir, chatID))
}

func resolveSandboxPaths(cfg config.Config, def catalog.AgentDefinition, localPaths contracts.LocalPaths) contracts.SandboxPaths {
	if cfg.IsLocalMode() {
		return resolveLocalSandboxPaths(cfg, def, localPaths)
	}
	return resolveContainerSandboxPaths(cfg, def)
}

func resolveContainerSandboxPaths(cfg config.Config, def catalog.AgentDefinition) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(anyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := def.AgentDir != ""
	hasSkillsDir := level != "global" && hasAgentDir

	var skillsMarketDir string
	ownerDir := ifNonEmpty(cfg.Paths.OwnerDir, "/owner")
	var agentsDir string
	var teamsDir string
	var automationsDir string
	var chatsDir string
	memoryDir := ifNonEmpty(cfg.Paths.MemoryDir, "/memory")
	var modelsDir string
	var providersDir string
	var mcpServersDir string
	var viewportServersDir string
	var toolsDir string
	var viewportsDir string
	for _, mount := range promptContextSandboxMounts(def.Runtime["sandboxMounts"]) {
		switch strings.ToLower(strings.TrimSpace(anyString(mount["platform"]))) {
		case "skills-market":
			skillsMarketDir = "/skills-market"
		case "agents":
			agentsDir = "/agents"
		case "teams":
			teamsDir = "/teams"
		case "automations":
			automationsDir = "/automations"
		case "chats":
			chatsDir = "/chats"
		case "models":
			modelsDir = "/models"
		case "providers":
			providersDir = "/providers"
		case "mcp-servers":
			mcpServersDir = "/mcp-servers"
		case "viewport-servers":
			viewportServersDir = "/viewport-servers"
		case "tools":
			toolsDir = "/tools"
		case "viewports":
			viewportsDir = "/viewports"
		}
	}

	return contracts.SandboxPaths{
		WorkspaceDir:       "/workspace",
		ChatDir:            "/chat",
		RootDir:            ifNonEmpty(cfg.Paths.RootDir, "/root"),
		SkillsDir:          boolPath(hasSkillsDir, "/skills"),
		SkillsMarketDir:    skillsMarketDir,
		PanDir:             ifNonEmpty(cfg.Paths.PanDir, "/pan"),
		AgentDir:           boolPath(hasAgentDir, "/agent"),
		OwnerDir:           ownerDir,
		AgentsDir:          agentsDir,
		TeamsDir:           teamsDir,
		AutomationsDir:     automationsDir,
		ChatsDir:           chatsDir,
		MemoryDir:          memoryDir,
		ModelsDir:          modelsDir,
		ProvidersDir:       providersDir,
		MCPServersDir:      mcpServersDir,
		ViewportServersDir: viewportServersDir,
		ToolsDir:           toolsDir,
		ViewportsDir:       viewportsDir,
	}
}

func resolveLocalSandboxPaths(cfg config.Config, def catalog.AgentDefinition, localPaths contracts.LocalPaths) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(anyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := strings.TrimSpace(def.AgentDir) != ""
	hasSkillsDir := level != "global" && hasAgentDir

	paths := contracts.SandboxPaths{
		WorkspaceDir: localPaths.WorkspaceDir,
		ChatDir:      localPaths.ChatDir,
		RootDir:      absOrEmpty(cfg.Paths.RootDir),
		SkillsDir:    resolveLocalSkillsDir(hasSkillsDir, level, def.AgentDir, cfg.Paths.SkillsMarketDir),
		PanDir:       absOrEmpty(cfg.Paths.PanDir),
		AgentDir:     absOrEmpty(def.AgentDir),
		OwnerDir:     absOrEmpty(cfg.Paths.OwnerDir),
		MemoryDir:    absOrEmpty(cfg.Paths.MemoryDir),
	}
	for _, mount := range promptContextSandboxMounts(def.Runtime["sandboxMounts"]) {
		switch strings.ToLower(strings.TrimSpace(anyString(mount["platform"]))) {
		case "skills-market":
			paths.SkillsMarketDir = absOrEmpty(cfg.Paths.SkillsMarketDir)
		case "agents":
			paths.AgentsDir = absOrEmpty(cfg.Paths.AgentsDir)
		case "teams":
			paths.TeamsDir = absOrEmpty(cfg.Paths.TeamsDir)
		case "automations":
			paths.AutomationsDir = absOrEmpty(cfg.Paths.AutomationsDir)
		case "chats":
			paths.ChatsDir = absOrEmpty(cfg.Paths.ChatsDir)
		case "models":
			paths.ModelsDir = absOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "models"))
		case "providers":
			paths.ProvidersDir = absOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "providers"))
		case "mcp-servers":
			paths.MCPServersDir = absOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers"))
		case "viewport-servers":
			paths.ViewportServersDir = absOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers"))
		case "tools":
			paths.ToolsDir = absOrEmpty(cfg.Paths.ToolsDir)
		case "viewports":
			paths.ViewportsDir = absOrEmpty(filepath.Join(filepath.Dir(filepath.Clean(cfg.Paths.RegistriesDir)), "viewports"))
		}
	}
	return paths
}

func buildAgentDigests(registry catalog.Registry) []contracts.AgentDigest {
	if registry == nil {
		return nil
	}
	items := registry.Agents("")
	digests := make([]contracts.AgentDigest, 0, len(items))
	for _, item := range items {
		def, ok := registry.AgentDefinition(item.Key)
		if !ok {
			// A registry may expose a summary while concurrently reloading its
			// definition. Keep the digest useful from the public summary instead
			// of relying on the list-only meta payload.
			def = catalog.AgentDefinition{
				Mode: item.Mode,
			}
		}
		digest := contracts.AgentDigest{
			Key:         item.Key,
			Name:        item.Name,
			Role:        item.Role,
			Description: item.Description,
			Mode:        def.Mode,
			ModelKey:    def.ModelKey,
			Tools:       append([]string(nil), def.Tools...),
			Skills:      append([]string(nil), def.Skills...),
		}
		environmentID := strings.TrimSpace(anyString(def.Runtime["environmentId"]))
		level := strings.TrimSpace(anyString(def.Runtime["level"]))
		if environmentID != "" || level != "" {
			digest.Sandbox = &contracts.SandboxDigest{
				EnvironmentID: environmentID,
				Level:         level,
			}
		}
		digests = append(digests, digest)
	}
	return digests
}

func buildContextAgentDigests(registry catalog.Registry, def catalog.AgentDefinition) ([]contracts.AgentDigest, error) {
	if !agentHasContextTag(def, "agents") {
		return nil, nil
	}
	digests := buildAgentDigests(registry)
	if len(def.ContextAgents) == 0 {
		return digests, nil
	}
	byKey := make(map[string]contracts.AgentDigest, len(digests))
	for _, digest := range digests {
		key := strings.TrimSpace(digest.Key)
		if key != "" {
			byKey[key] = digest
		}
	}
	filtered := make([]contracts.AgentDigest, 0, len(def.ContextAgents))
	for _, agentKey := range def.ContextAgents {
		agentKey = strings.TrimSpace(agentKey)
		if agentKey == "" {
			continue
		}
		digest, ok := byKey[agentKey]
		if !ok {
			return nil, fmt.Errorf("contextConfig.agents contains unknown agent key %q", agentKey)
		}
		filtered = append(filtered, digest)
	}
	return filtered, nil
}

func agentHasContextTag(def catalog.AgentDefinition, tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return false
	}
	for _, configured := range def.ContextTags {
		configured = strings.ToLower(strings.TrimSpace(configured))
		if configured == tag {
			return true
		}
	}
	return false
}

func buildAuthIdentity(principal *Principal) *contracts.AuthIdentity {
	if principal == nil {
		return nil
	}
	identity := &contracts.AuthIdentity{
		Subject:  principal.Subject,
		DeviceID: firstStringClaim(principal.Claims, "deviceId", "device_id"),
		Scope:    firstStringClaim(principal.Claims, "scope"),
	}
	if issuedAt := numericDate(principal.Claims["iat"]); issuedAt > 0 {
		identity.IssuedAt = time.Unix(issuedAt, 0).UTC().Format(time.RFC3339)
	}
	if expiresAt := numericDate(principal.Claims["exp"]); expiresAt > 0 {
		identity.ExpiresAt = time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
	}
	return identity
}

func buildSandboxContext(cfg config.Config, def catalog.AgentDefinition) (*contracts.SandboxContext, error) {
	configuredEnvironmentID := strings.TrimSpace(anyString(def.Runtime["environmentId"]))
	defaultEnvironmentID := strings.TrimSpace(cfg.ContainerHub.DefaultEnvironmentID)
	environmentID := configuredEnvironmentID
	if environmentID == "" {
		environmentID = defaultEnvironmentID
	}
	if environmentID == "" {
		return nil, fmt.Errorf("sandbox context requires a non-blank environmentId")
	}

	level := strings.ToUpper(strings.TrimSpace(anyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToUpper(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "RUN"
	}

	prompt, err := fetchSandboxPrompt(cfg.ContainerHub, environmentID)
	if err != nil {
		return nil, err
	}
	return &contracts.SandboxContext{
		EnvironmentID:           environmentID,
		ConfiguredEnvironmentID: configuredEnvironmentID,
		DefaultEnvironmentID:    defaultEnvironmentID,
		Level:                   level,
		ContainerHubEnabled:     cfg.ContainerHub.Enabled,
		UsesSandboxBash:         hasRuntimeSandbox(def.Runtime),
		ExtraMounts:             summarizeSandboxMounts(def),
		EnvironmentPrompt:       prompt,
	}, nil
}

func fetchSandboxPrompt(cfg config.ContainerHubConfig, environmentID string) (string, error) {
	if !cfg.Enabled {
		return "", fmt.Errorf("sandbox context requires container-hub client availability")
	}
	result, err := sandbox.NewContainerHubClient(cfg).GetEnvironmentAgentPrompt(environmentID)
	if err != nil {
		return "", fmt.Errorf("sandbox context failed to load environment prompt for %q: %w", environmentID, err)
	}
	if !result.OK {
		return "", fmt.Errorf("sandbox context failed to load environment prompt for %q: %s", environmentID, result.Error)
	}
	if !result.HasPrompt || strings.TrimSpace(result.Prompt) == "" {
		if strings.EqualFold(environmentID, "shell") {
			return "", nil
		}
		return "", fmt.Errorf("sandbox context requires a non-blank environment prompt for %q", environmentID)
	}
	return strings.TrimSpace(result.Prompt), nil
}

func summarizeSandboxMounts(def catalog.AgentDefinition) []string {
	mounts := promptContextSandboxMounts(def.Runtime["sandboxMounts"])
	out := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mode := strings.ToLower(strings.TrimSpace(anyString(mount["mode"])))
		if mode == "" {
			mode = "unspecified"
		}
		platform := strings.TrimSpace(anyString(mount["platform"]))
		source := strings.TrimSpace(anyString(mount["source"]))
		destination := strings.TrimSpace(anyString(mount["destination"]))
		switch {
		case platform != "":
			out = append(out, "platform:"+platform+" ("+mode+")")
		case source != "" && destination != "":
			out = append(out, source+" -> "+destination+" ("+mode+")")
		case destination != "":
			out = append(out, "destination:"+destination+" ("+mode+")")
		}
	}
	return out
}

func firstStringClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(anyString(claims[key])); value != "" {
			return value
		}
	}
	return ""
}

func promptContextSandboxMounts(value any) []map[string]any {
	var out []map[string]any
	switch mounts := value.(type) {
	case []map[string]any:
		out = append(out, mounts...)
	case []any:
		for _, raw := range mounts {
			if mount, ok := raw.(map[string]any); ok {
				out = append(out, mount)
			}
		}
	}
	return out
}

func anyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func cleanOrEmpty(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}

func absOrEmpty(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	clean := filepath.Clean(path)
	absolute, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	return absolute
}

func resolveLocalSkillsDir(hasSkillsDir bool, level string, agentDir string, skillsMarketDir string) string {
	if !hasSkillsDir {
		return ""
	}
	if level != "global" && strings.TrimSpace(agentDir) != "" {
		return absOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return ""
}

func promptContextHasPlatformMount(sandboxMounts any, platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return false
	}
	for _, mount := range promptContextSandboxMounts(sandboxMounts) {
		if strings.EqualFold(strings.TrimSpace(anyString(mount["platform"])), platform) {
			return true
		}
	}
	return false
}

func ifNonEmpty(path string, target string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return target
}

func boolPath(ok bool, target string) string {
	if !ok {
		return ""
	}
	return target
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == strings.TrimSpace(needle) {
			return true
		}
	}
	return false
}
