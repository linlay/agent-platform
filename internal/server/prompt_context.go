package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/sandbox"
)

func buildPromptAppendConfig(global config.PromptsConfig, def catalog.AgentDefinition) contracts.PromptAppendConfig {
	config := contracts.DefaultPromptAppendConfig()
	if strings.TrimSpace(global.Skill.InstructionsPrompt) != "" {
		config.Skill.InstructionsPrompt = strings.TrimSpace(global.Skill.InstructionsPrompt)
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
	context := contracts.RuntimeRequestContext{
		AgentKey:     input.agentKey,
		TeamID:       input.teamID,
		Role:         input.role,
		ChatName:     input.chatName,
		LocalMode:    s.deps.Config.IsLocalMode(),
		Scene:        input.scene,
		References:   append([]api.Reference(nil), input.references...),
		LocalPaths:   resolveLocalPaths(s.deps.Config.Paths, input.chatID, input.definition.AgentDir),
		SandboxPaths: resolveSandboxPaths(s.deps.Config, input.definition, input.chatID),
		AgentDigests: buildAgentDigests(s.deps.Registry),
	}
	if input.principal != nil {
		context.AuthIdentity = buildAuthIdentity(input.principal)
	}
	if hasSandboxConfig(input.definition.Sandbox) && s.deps.Config.ContainerHub.Enabled {
		sandboxContext, err := buildSandboxContext(s.deps.Config, input.definition)
		if err != nil {
			return contracts.RuntimeRequestContext{}, err
		}
		context.SandboxContext = sandboxContext
	}
	return context, nil
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
		sections = append(sections, instructionsPrompt)
	}
	sections = append(sections, strings.TrimSpace(appendConfig.Skill.CatalogHeader))
	sections = append(sections, strings.Join(blocks, "\n\n---\n\n"))
	return strings.Join(sections, "\n\n")
}

func resolveLocalPaths(paths config.PathsConfig, chatID string, agentDir string) contracts.LocalPaths {
	runtimeHome := filepath.Dir(filepath.Clean(paths.AgentsDir))
	workingDirectory, _ := os.Getwd()
	attachmentsDir := cleanOrEmpty(filepath.Join(paths.ChatsDir, strings.TrimSpace(chatID)))
	agentDir = cleanOrEmpty(agentDir)
	agentSkillsDir := ""
	if agentDir != "" {
		agentSkillsDir = cleanOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return contracts.LocalPaths{
		RuntimeHome:        runtimeHome,
		WorkingDirectory:   cleanOrEmpty(workingDirectory),
		RootDir:            cleanOrEmpty(paths.RootDir),
		PanDir:             cleanOrEmpty(paths.PanDir),
		AgentDir:           agentDir,
		AgentsDir:          cleanOrEmpty(paths.AgentsDir),
		TeamsDir:           cleanOrEmpty(paths.TeamsDir),
		ChatsDir:           cleanOrEmpty(paths.ChatsDir),
		MemoryDir:          cleanOrEmpty(paths.MemoryDir),
		DataDir:            cleanOrEmpty(paths.ChatsDir),
		SkillsDir:          agentSkillsDir,
		SkillsMarketDir:    cleanOrEmpty(paths.SkillsMarketDir),
		SchedulesDir:       cleanOrEmpty(paths.SchedulesDir),
		OwnerDir:           cleanOrEmpty(paths.OwnerDir),
		ModelsDir:          cleanOrEmpty(filepath.Join(paths.RegistriesDir, "models")),
		ProvidersDir:       cleanOrEmpty(filepath.Join(paths.RegistriesDir, "providers")),
		MCPServersDir:      cleanOrEmpty(filepath.Join(paths.RegistriesDir, "mcp-servers")),
		ViewportServersDir: cleanOrEmpty(filepath.Join(paths.RegistriesDir, "viewport-servers")),
		ToolsDir:           cleanOrEmpty(paths.ToolsDir),
		ViewportsDir:       cleanOrEmpty(filepath.Join(paths.RegistriesDir, "viewports")),
		ChatAttachmentsDir: attachmentsDir,
	}
}

func resolveSandboxPaths(cfg config.Config, def catalog.AgentDefinition, chatID string) contracts.SandboxPaths {
	if cfg.IsLocalMode() {
		return resolveLocalSandboxPaths(cfg, def, chatID)
	}
	return resolveContainerSandboxPaths(cfg, def, chatID)
}

func resolveContainerSandboxPaths(cfg config.Config, def catalog.AgentDefinition, chatID string) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(anyString(def.Sandbox["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := def.AgentDir != ""
	hasGlobalSkillsDir := strings.TrimSpace(cfg.Paths.SkillsMarketDir) != ""
	hasSkillsDir := hasGlobalSkillsDir
	if level != "global" && hasAgentDir {
		hasSkillsDir = true
	}

	var skillsMarketDir string
	ownerDir := ifNonEmpty(cfg.Paths.OwnerDir, "/owner")
	var agentsDir string
	var teamsDir string
	var schedulesDir string
	var chatsDir string
	memoryDir := ifNonEmpty(cfg.Paths.MemoryDir, "/memory")
	var modelsDir string
	var providersDir string
	var mcpServersDir string
	var viewportServersDir string
	var toolsDir string
	var viewportsDir string
	for _, mount := range promptContextSandboxMounts(def.Sandbox["extraMounts"]) {
		switch strings.ToLower(strings.TrimSpace(anyString(mount["platform"]))) {
		case "skills-market":
			skillsMarketDir = "/skills-market"
		case "agents":
			agentsDir = "/agents"
		case "teams":
			teamsDir = "/teams"
		case "schedules":
			schedulesDir = "/schedules"
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
		Cwd:                "/workspace",
		WorkspaceDir:       "/workspace",
		RootDir:            ifNonEmpty(cfg.Paths.RootDir, "/root"),
		SkillsDir:          boolPath(hasSkillsDir, "/skills"),
		SkillsMarketDir:    skillsMarketDir,
		PanDir:             ifNonEmpty(cfg.Paths.PanDir, "/pan"),
		AgentDir:           boolPath(hasAgentDir, "/agent"),
		OwnerDir:           ownerDir,
		AgentsDir:          agentsDir,
		TeamsDir:           teamsDir,
		SchedulesDir:       schedulesDir,
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

func resolveLocalSandboxPaths(cfg config.Config, def catalog.AgentDefinition, chatID string) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(anyString(def.Sandbox["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := strings.TrimSpace(def.AgentDir) != ""
	hasGlobalSkillsDir := strings.TrimSpace(cfg.Paths.SkillsMarketDir) != ""
	hasSkillsDir := hasGlobalSkillsDir
	if level != "global" && hasAgentDir {
		hasSkillsDir = true
	}

	workspaceDir := resolveLocalWorkspaceDir(cfg.Paths, chatID)
	paths := contracts.SandboxPaths{
		Cwd:          workspaceDir,
		WorkspaceDir: workspaceDir,
		RootDir:      absOrEmpty(cfg.Paths.RootDir),
		SkillsDir:    resolveLocalSkillsDir(hasSkillsDir, level, def.AgentDir, cfg.Paths.SkillsMarketDir),
		PanDir:       absOrEmpty(cfg.Paths.PanDir),
		AgentDir:     absOrEmpty(def.AgentDir),
		OwnerDir:     absOrEmpty(cfg.Paths.OwnerDir),
		MemoryDir:    absOrEmpty(cfg.Paths.MemoryDir),
	}
	for _, mount := range promptContextSandboxMounts(def.Sandbox["extraMounts"]) {
		switch strings.ToLower(strings.TrimSpace(anyString(mount["platform"]))) {
		case "skills-market":
			paths.SkillsMarketDir = absOrEmpty(cfg.Paths.SkillsMarketDir)
		case "agents":
			paths.AgentsDir = absOrEmpty(cfg.Paths.AgentsDir)
		case "teams":
			paths.TeamsDir = absOrEmpty(cfg.Paths.TeamsDir)
		case "schedules":
			paths.SchedulesDir = absOrEmpty(cfg.Paths.SchedulesDir)
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
			paths.ViewportsDir = absOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "viewports"))
		}
	}
	return paths
}

func buildAgentDigests(registry catalog.Registry) []contracts.AgentDigest {
	items := registry.Agents("")
	digests := make([]contracts.AgentDigest, 0, len(items))
	for _, item := range items {
		meta := item.Meta
		digest := contracts.AgentDigest{
			Key:         item.Key,
			Name:        item.Name,
			Role:        item.Role,
			Description: item.Description,
			Mode:        stringMeta(meta, "mode"),
			ModelKey:    stringMeta(meta, "model"),
			Tools:       listMeta(meta, "tools"),
			Skills:      listMeta(meta, "skills"),
		}
		if sandbox, ok := meta["sandbox"].(map[string]any); ok {
			environmentID := strings.TrimSpace(anyString(sandbox["environmentId"]))
			level := strings.TrimSpace(anyString(sandbox["level"]))
			if environmentID != "" || level != "" {
				digest.Sandbox = &contracts.SandboxDigest{
					EnvironmentID: environmentID,
					Level:         level,
				}
			}
		}
		digests = append(digests, digest)
	}
	return digests
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
	configuredEnvironmentID := strings.TrimSpace(anyString(def.Sandbox["environmentId"]))
	defaultEnvironmentID := strings.TrimSpace(cfg.ContainerHub.DefaultEnvironmentID)
	environmentID := configuredEnvironmentID
	if environmentID == "" {
		environmentID = defaultEnvironmentID
	}
	if environmentID == "" {
		return nil, fmt.Errorf("sandbox context requires a non-blank environmentId")
	}

	level := strings.ToUpper(strings.TrimSpace(anyString(def.Sandbox["level"])))
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
		UsesSandboxBash:         hasSandboxConfig(def.Sandbox),
		ExtraMounts:             summarizeExtraMounts(def),
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

func summarizeExtraMounts(def catalog.AgentDefinition) []string {
	mounts := promptContextSandboxMounts(def.Sandbox["extraMounts"])
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

func listMeta(meta map[string]any, key string) []string {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			trimmed := strings.TrimSpace(anyString(value))
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMeta(meta map[string]any, key string) string {
	return strings.TrimSpace(anyString(meta[key]))
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

func resolveLocalWorkspaceDir(paths config.PathsConfig, chatID string) string {
	if strings.TrimSpace(chatID) != "" {
		return absOrEmpty(filepath.Join(paths.ChatsDir, strings.TrimSpace(chatID)))
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ""
	}
	return absOrEmpty(workingDirectory)
}

func resolveLocalSkillsDir(hasSkillsDir bool, level string, agentDir string, skillsMarketDir string) string {
	if !hasSkillsDir {
		return ""
	}
	if level != "global" && strings.TrimSpace(agentDir) != "" {
		return absOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return absOrEmpty(skillsMarketDir)
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
