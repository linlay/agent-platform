package session

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/sandbox"
)

func BuildPromptAppendConfig(global config.PromptsConfig, def catalog.AgentDefinition) contracts.PromptAppendConfig {
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

type ContextInput struct {
	AgentKey           string
	TeamID             string
	Role               string
	ChatID             string
	ChatName           string
	Scene              *queryinput.Scene
	References         []queryinput.Reference
	Principal          *contracts.AuthIdentity
	Definition         catalog.AgentDefinition
	ExposeSkillsCenter bool
}

func (s *Builder) BuildContext(input ContextInput) (contracts.RuntimeRequestContext, error) {
	workspaceRoot := EffectiveLocalWorkspaceRoot(input.Definition)
	if strings.EqualFold(catalog.NormalizeAgentModeForRuntime(input.Definition.Mode), catalog.AgentModeCoder) &&
		strings.TrimSpace(workspaceRoot) == "" {
		return contracts.RuntimeRequestContext{}, fmt.Errorf("workspace_unavailable: CODER requires a workspace")
	}
	if HasRuntimeSandbox(input.Definition.Runtime) && strings.TrimSpace(workspaceRoot) == "" {
		return contracts.RuntimeRequestContext{}, fmt.Errorf("workspace_unavailable: Container Hub sandbox requires a workspace")
	}
	localPaths, err := ResolveLocalPaths(s.deps.Config.Paths, input.ChatID, input.Definition.RuntimeDir, workspaceRoot)
	if err != nil {
		return contracts.RuntimeRequestContext{}, err
	}
	if input.ExposeSkillsCenter || PromptContextHasPlatformMount(input.Definition.Runtime["sandboxMounts"], "skills-center") {
		localPaths.SkillsCenterDir = CleanOrEmpty(s.deps.Config.Paths.SkillsCenterDir)
	}
	references, err := s.NormalizeReferences(input.References, input.ChatID, input.Definition, localPaths)
	if err != nil {
		return contracts.RuntimeRequestContext{}, err
	}
	sandboxPaths := ResolveSandboxPaths(s.deps.Config, input.Definition, localPaths)
	if input.ExposeSkillsCenter {
		if s.deps.Config.IsLocalMode() {
			sandboxPaths.SkillsCenterDir = localPaths.SkillsCenterDir
		} else {
			sandboxPaths.SkillsCenterDir = "/skills-center"
		}
	}
	context := contracts.RuntimeRequestContext{
		AgentKey:     input.AgentKey,
		TeamID:       input.TeamID,
		Role:         input.Role,
		ChatName:     input.ChatName,
		LocalMode:    s.deps.Config.IsLocalMode(),
		Scene:        input.Scene,
		References:   references,
		LocalPaths:   localPaths,
		SandboxPaths: sandboxPaths,
	}
	agentDigests, diagnostic := BuildContextAgentDigests(s.deps.Registry, input.Definition, input.AgentKey)
	if diagnostic != nil {
		log.Printf("[runtime][context-agents][warn] code=%s agent=%q %s", diagnostic.Code, fmt.Sprintf("%.128s", input.AgentKey), diagnostic.Message)
	}
	context.AgentDigests = agentDigests
	if input.Principal != nil {
		context.AuthIdentity = BuildAuthIdentity(input.Principal)
	}
	if HasRuntimeSandbox(input.Definition.Runtime) && s.deps.Config.ContainerHub.Enabled {
		sandboxContext, err := BuildSandboxContext(s.deps.Config, input.Definition)
		if err != nil {
			return contracts.RuntimeRequestContext{}, err
		}
		context.SandboxContext = sandboxContext
	}
	return context, nil
}

func (s *Builder) NormalizeReferences(
	references []queryinput.Reference,
	chatID string,
	def catalog.AgentDefinition,
	localPaths contracts.LocalPaths,
) ([]queryinput.Reference, error) {
	if len(references) == 0 {
		return references, nil
	}
	normalized := append([]queryinput.Reference(nil), references...)
	for i := range normalized {
		path, err := s.ReferencePath(normalized[i], chatID, def, localPaths)
		if err != nil {
			return nil, err
		}
		if path != "" {
			normalized[i].Path = path
		}
	}
	return normalized, nil
}

func (s *Builder) ReferencePath(
	reference queryinput.Reference,
	chatID string,
	def catalog.AgentDefinition,
	localPaths contracts.LocalPaths,
) (string, error) {
	switch strings.ToLower(strings.TrimSpace(reference.Type)) {
	case "chat", "site", "selection":
		return "", nil
	}
	if ResourceFileParamForChat(chatID, reference.URL) == "" {
		rawPath := strings.TrimSpace(reference.Path)
		if rawPath == "/workspace" || strings.HasPrefix(rawPath, "/workspace/") {
			return "", fmt.Errorf("path-only /workspace references are not accepted; re-materialize the file through the resource API")
		}
	}
	if s.AgentUsesContainerHub(def) {
		if fileParam := ResourceFileParamForChat(chatID, reference.URL); fileParam != "" {
			rel, ok := CurrentChatResourceRelativePath(chatID, fileParam)
			if !ok {
				return "", fmt.Errorf("reference resource must be materialized in the current chat before Container Hub execution")
			}
			return "/chat/" + filepath.ToSlash(rel), nil
		}
		return TranslateReferencePathForContainer(reference.Path, localPaths)
	}
	if fileParam := ResourceFileParamForChat(chatID, reference.URL); fileParam != "" && s != nil && s.deps.Chats != nil {
		if path, err := s.deps.Chats.ResolveResource(fileParam); err == nil {
			return path, nil
		}
	}
	if strings.TrimSpace(reference.Path) != "" {
		return TranslateReferencePathForHost(reference.Path, localPaths)
	}
	if rel := ReferenceResourceRelativePath(chatID, reference); rel != "" && s != nil && s.deps.Chats != nil {
		return filepath.Join(s.deps.Chats.ChatDir(chatID), filepath.FromSlash(rel)), nil
	}
	return "", nil
}

func TranslateReferencePathForHost(rawPath string, localPaths contracts.LocalPaths) (string, error) {
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
				return RequireReferenceWorkspacePath(resolved, localPaths)
			}
			return resolved, nil
		}
		prefix := item.alias + "/"
		if strings.HasPrefix(strings.ToLower(normalized), prefix) {
			if strings.TrimSpace(item.root) == "" {
				return "", fmt.Errorf("%s_unavailable: reference root is unavailable", strings.TrimPrefix(item.alias, "@"))
			}
			resolved, err := ReferencePathWithinHostRoot(item.root, normalized[len(prefix):], item.alias)
			if err != nil || item.alias != "@workspace" {
				return resolved, err
			}
			return RequireReferenceWorkspacePath(resolved, localPaths)
		}
	}
	if rawPath == "/chat" || strings.HasPrefix(rawPath, "/chat/") {
		return ReferencePathWithinHostRoot(localPaths.ChatDir, strings.TrimLeft(strings.TrimPrefix(rawPath, "/chat"), "/"), "@chat")
	}
	if !filepath.IsAbs(pathutil.ExpandHome(rawPath)) {
		if strings.TrimSpace(localPaths.WorkspaceDir) == "" {
			return "", fmt.Errorf("workspace_unavailable: relative reference path requires a workspace")
		}
		resolved, err := ReferencePathWithinHostRoot(localPaths.WorkspaceDir, rawPath, "@workspace")
		if err != nil {
			return "", err
		}
		return RequireReferenceWorkspacePath(resolved, localPaths)
	}
	roots, err := LocalSemanticRoots(localPaths)
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

func ReferencePathWithinHostRoot(root string, suffix string, alias string) (string, error) {
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

func TranslateReferencePathForContainer(rawPath string, localPaths contracts.LocalPaths) (string, error) {
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
	hostPath, err := TranslateReferencePathForHost(rawPath, localPaths)
	if err != nil {
		return "", err
	}
	roots, err := LocalSemanticRoots(localPaths)
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

func LocalSemanticRoots(localPaths contracts.LocalPaths) (rootpaths.Roots, error) {
	return rootpaths.New(localPaths.WorkspaceDir, localPaths.ChatsDir, localPaths.ChatDir)
}

func RequireReferenceWorkspacePath(candidate string, localPaths contracts.LocalPaths) (string, error) {
	roots, err := LocalSemanticRoots(localPaths)
	if err != nil {
		return "", err
	}
	resolved, err := roots.RequireWorkspacePath(candidate)
	if err != nil {
		return "", err
	}
	return resolved.Host, nil
}

func CurrentChatResourceRelativePath(chatID string, fileParam string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(fileParam))
	prefix := strings.TrimSpace(chatID) + "/"
	if strings.TrimSpace(chatID) == "" || !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(clean, prefix)
	return rel, rel != "" && rel != "."
}

func ReferenceResourceRelativePath(chatID string, reference queryinput.Reference) string {
	if fileParam := ResourceFileParamForChat(chatID, reference.URL); fileParam != "" {
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
	return ReferenceName(reference)
}

func ReferenceName(reference queryinput.Reference) string {
	for _, candidate := range []string{
		reference.Name,
		ResourceFileName(reference.URL),
	} {
		name := filepath.Base(filepath.ToSlash(strings.TrimSpace(candidate)))
		if name != "" && name != "." && name != "/" {
			return name
		}
	}
	return ""
}

func ResourceFileName(rawURL string) string {
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

type SkillCenterCatalog interface {
	SkillKeys() []string
	SkillDefinition(key string) (catalog.SkillDefinition, bool)
}

type MustUseSkill struct {
	Key              string
	InstructionsPath string
	RootPath         string
	Extra            bool
	Definition       catalog.SkillDefinition
}

type SkillResolution struct {
	Skills         []MustUseSkill
	Keys           []string
	HasExtraSkills bool
}

func MustUseSkillUnavailableStatus(err error) *runtimetypes.RequestError {
	const code = "must_use_skill_unavailable"
	message := "must-use skill is unavailable"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return &runtimetypes.RequestError{
		Status:  400,
		Code:    code,
		Message: message,
		Data: map[string]any{
			"error": map[string]any{"code": code, "message": message},
		},
	}
}

func BuildSkillCatalogPrompt(def catalog.AgentDefinition, centerDir string, appendConfig contracts.PromptAppendConfig, mustUseSkills ...MustUseSkill) string {
	_ = centerDir
	if len(def.EffectiveSkills()) == 0 && len(mustUseSkills) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(def.EffectiveSkills())+len(mustUseSkills))
	seen := map[string]struct{}{}
	for _, configuredSkill := range def.EffectiveSkills() {
		skillID := strings.ToLower(strings.TrimSpace(configuredSkill))
		if skillID == "" {
			continue
		}
		if _, ok := seen[skillID]; ok {
			continue
		}
		seen[skillID] = struct{}{}
		definition, ok, err := def.ResolveSkillDefinition(skillID)
		if err != nil {
			log.Printf("[server][skill-catalog][warn] resolve skill %s failed: %v", skillID, err)
			continue
		}
		if !ok {
			continue
		}
		blocks = append(blocks, SkillCatalogBlock(definition, def.SkillInstructionsPath(definition.Key)))
	}
	for _, skill := range mustUseSkills {
		normalized := strings.ToLower(strings.TrimSpace(skill.Key))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		blocks = append(blocks, SkillCatalogBlock(skill.Definition, skill.InstructionsPath))
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
	sections = append(sections, `Skill loading contract:
- Check all catalog entries for applicability, including connector skills. When a listed skill applies or the user names it, read its exact path with file_read before acting or running its CLI; do not wait for the user to ask you to read it.
- Each catalog path points to a SKILL.md file. Copy its value verbatim into file_read.file_path. @skills, @skills-center, and @connectors are distinct semantic roots accepted directly by file_read; do not replace the prefix, derive a path from skillId, or guess an absolute path.
- A path under @connectors/<id>/... resolves inside the current Agent's mounted connector package. Pass the entire value directly to file_read.file_path. CLI availability does not mean its skill instructions have been read.
- If a read fails, compare the attempted path with the catalog and retry with the exact path if they differ. If the exact path fails, report that failure; do not substitute a same-named copy from another root or claim the skill was read successfully.
- Do not use Bash, directory traversal, or filesystem search to discover installed skill locations. Resolve relative references inside a skill against the directory containing its exact path.`)
	sections = append(sections, strings.TrimSpace(appendConfig.Skill.CatalogHeader))
	sections = append(sections, strings.Join(blocks, "\n\n---\n\n"))
	return strings.Join(sections, "\n\n")
}

func SkillCatalogBlock(definition catalog.SkillDefinition, skillPath string) string {
	lines := []string{
		"skillId: " + definition.Key,
		"path: " + strings.TrimSpace(skillPath),
	}
	if strings.TrimSpace(definition.Name) != "" {
		lines = append(lines, "name: "+strings.TrimSpace(definition.Name))
	}
	if strings.TrimSpace(definition.Description) != "" {
		lines = append(lines, "description: "+strings.TrimSpace(definition.Description))
	}
	return strings.Join(lines, "\n")
}

func NormalizeMustUseSkills(requested []string) []string {
	if len(requested) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(requested))
	seen := map[string]struct{}{}
	for _, key := range requested {
		trimmed := strings.TrimSpace(key)
		normalized := strings.ToLower(trimmed)
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		resolved = append(resolved, trimmed)
	}
	return resolved
}

func ResolveMustUseSkills(def catalog.AgentDefinition, centerDir string, center SkillCenterCatalog, requested []string) (SkillResolution, error) {
	normalizedRequested := NormalizeMustUseSkills(requested)
	if len(normalizedRequested) == 0 {
		return SkillResolution{}, nil
	}
	configured := make(map[string]string, len(def.Skills))
	for _, key := range def.Skills {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			configured[strings.ToLower(trimmed)] = trimmed
		}
	}
	result := SkillResolution{
		Skills: make([]MustUseSkill, 0, len(normalizedRequested)),
		Keys:   make([]string, 0, len(normalizedRequested)),
	}
	for _, requestedKey := range normalizedRequested {
		if def.IsConnectorSkill(requestedKey) || connector.IsReservedSkill(requestedKey) {
			return SkillResolution{}, fmt.Errorf("connector skill %q cannot be selected by mustUseSkills", requestedKey)
		}
		normalized := strings.ToLower(requestedKey)
		if configuredKey, ok := configured[normalized]; ok {
			rootPath, err := ResolveMustUseSkillRoot(filepath.Join(def.RuntimeDir, "skills"), configuredKey)
			if err != nil {
				return SkillResolution{}, fmt.Errorf("resolve must-use skill %q root: %w", configuredKey, err)
			}
			definition, found, err := catalog.ResolveRuntimeSkillDefinition(def.RuntimeDir, configuredKey)
			if err != nil {
				return SkillResolution{}, fmt.Errorf("resolve must-use skill %q: %w", configuredKey, err)
			}
			if !found {
				return SkillResolution{}, fmt.Errorf("must-use skill %q could not be resolved from agent runtime", configuredKey)
			}
			result.Skills = append(result.Skills, MustUseSkill{
				Key:              definition.Key,
				InstructionsPath: "@skills/" + definition.Key + "/SKILL.md",
				RootPath:         rootPath,
				Definition:       definition,
			})
			result.Keys = append(result.Keys, definition.Key)
			continue
		}

		centerKey, ok := ResolveCenterSkillKey(center, requestedKey)
		if !ok {
			return SkillResolution{}, fmt.Errorf("must-use skill %q is unavailable in the active skills center", requestedKey)
		}
		rootPath, err := ResolveMustUseSkillRoot(centerDir, centerKey)
		if err != nil {
			return SkillResolution{}, fmt.Errorf("resolve must-use center skill %q root: %w", centerKey, err)
		}
		definition, found, err := catalog.ResolveSkillDefinition("", centerDir, centerKey)
		if err != nil {
			return SkillResolution{}, fmt.Errorf("resolve must-use center skill %q: %w", centerKey, err)
		}
		if !found {
			return SkillResolution{}, fmt.Errorf("must-use skill %q could not be resolved from the skills center", centerKey)
		}
		result.Skills = append(result.Skills, MustUseSkill{
			Key:              definition.Key,
			InstructionsPath: "@skills-center/" + definition.Key + "/SKILL.md",
			RootPath:         rootPath,
			Extra:            true,
			Definition:       definition,
		})
		result.Keys = append(result.Keys, definition.Key)
		result.HasExtraSkills = true
	}
	return result, nil
}

func ResolveMustUseSkillRoot(parentDir string, skillKey string) (string, error) {
	parentDir = strings.TrimSpace(parentDir)
	skillKey = strings.TrimSpace(skillKey)
	if parentDir == "" || skillKey == "" {
		return "", fmt.Errorf("skill root is unavailable")
	}
	parent, err := pathutil.Canonicalize(parentDir)
	if err != nil {
		return "", err
	}
	root, err := pathutil.Canonicalize(filepath.Join(parent.Host, skillKey))
	if err != nil {
		return "", err
	}
	if !pathutil.WithinRoot(root, parent) || root.Key == parent.Key {
		return "", fmt.Errorf("skill root escapes its expected parent")
	}
	info, err := os.Stat(root.Host)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skill root is not a directory")
	}
	return root.Host, nil
}

func MustUseSkillRunAccess(skills []MustUseSkill) (contracts.RunAccessRoots, error) {
	if len(skills) == 0 {
		return contracts.RunAccessRoots{}, nil
	}
	readRoots := make([]string, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		root, err := pathutil.Canonicalize(strings.TrimSpace(skill.RootPath))
		if err != nil {
			return contracts.RunAccessRoots{}, fmt.Errorf("resolve must-use skill %q run access root: %w", skill.Key, err)
		}
		info, err := os.Stat(root.Host)
		if err != nil {
			return contracts.RunAccessRoots{}, fmt.Errorf("resolve must-use skill %q run access root: %w", skill.Key, err)
		}
		if !info.IsDir() {
			return contracts.RunAccessRoots{}, fmt.Errorf("resolve must-use skill %q run access root: not a directory", skill.Key)
		}
		if _, duplicate := seen[root.Key]; duplicate {
			continue
		}
		seen[root.Key] = struct{}{}
		readRoots = append(readRoots, root.Host)
	}
	return contracts.RunAccessRoots{
		ReadRoots:     append([]string(nil), readRoots...),
		ReadonlyRoots: append([]string(nil), readRoots...),
	}, nil
}

func (s *Builder) ResolveSkills(def catalog.AgentDefinition, requested []string) (SkillResolution, error) {
	normalized := NormalizeMustUseSkills(requested)
	if IsProxyRoutedAgent(def) {
		return SkillResolution{Keys: normalized}, nil
	}
	return ResolveMustUseSkills(def, s.deps.Config.Paths.SkillsCenterDir, s.deps.Registry, normalized)
}

func ResolveCenterSkillKey(center SkillCenterCatalog, requested string) (string, bool) {
	if center == nil {
		return "", false
	}
	requested = strings.TrimSpace(requested)
	for _, key := range center.SkillKeys() {
		if !strings.EqualFold(strings.TrimSpace(key), requested) {
			continue
		}
		definition, ok := center.SkillDefinition(key)
		if !ok || strings.TrimSpace(definition.Key) == "" {
			return "", false
		}
		return definition.Key, true
	}
	return "", false
}

func BuildMustUseSkillConstraint(skills []MustUseSkill) string {
	if len(skills) == 0 {
		return ""
	}
	lines := []string{
		"Must-use skills for this run:",
	}
	for _, skill := range skills {
		if key := strings.TrimSpace(skill.Key); key != "" {
			lines = append(lines, "- skillId: "+key+"\n  path: "+strings.TrimSpace(skill.InstructionsPath))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(
		lines,
		"You must read the complete SKILL.md at every path above and follow all of them for this run. None may be skipped, silently ignored, or replaced by another skill.",
	)
	return strings.Join(lines, "\n")
}

func EffectiveLocalWorkspaceRoot(def catalog.AgentDefinition) string {
	return strings.TrimSpace(def.Workspace.Root)
}

func ResolveLocalPaths(paths config.PathsConfig, chatID string, agentDir string, workspaceRoot string) (contracts.LocalPaths, error) {
	ruAgentsDir := paths.EffectiveRUAgentsDir()
	runtimeHome := filepath.Dir(filepath.Clean(ruAgentsDir))
	var err error
	workspaceRoot, err = ResolveHostWorkspaceRoot(workspaceRoot)
	if err != nil {
		return contracts.LocalPaths{}, err
	}
	if err := ValidateWorkspaceChatsSeparation(workspaceRoot, paths.ChatsDir); err != nil {
		return contracts.LocalPaths{}, err
	}
	chatDir, err := EnsureChatDir(paths, chatID)
	if err != nil {
		return contracts.LocalPaths{}, err
	}
	agentDir = CleanOrEmpty(agentDir)
	agentSkillsDir := ""
	if agentDir != "" {
		agentSkillsDir = CleanOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return contracts.LocalPaths{
		RuntimeHome:         runtimeHome,
		WorkspaceDir:        workspaceRoot,
		ChatDir:             chatDir,
		RootDir:             CleanOrEmpty(paths.RootDir),
		PanDir:              CleanOrEmpty(paths.PanDir),
		AgentDir:            agentDir,
		AgentsDir:           CleanOrEmpty(paths.AgentsDir),
		RUAgentsDir:         CleanOrEmpty(ruAgentsDir),
		TeamsDir:            CleanOrEmpty(paths.TeamsDir),
		ChatsDir:            CleanOrEmpty(paths.ChatsDir),
		MemoryDir:           CleanOrEmpty(paths.MemoryDir),
		SkillsDir:           agentSkillsDir,
		AutomationsDir:      CleanOrEmpty(paths.AutomationsDir),
		OwnerDir:            CleanOrEmpty(paths.OwnerDir),
		ModelsDir:           CleanOrEmpty(filepath.Join(paths.RegistriesDir, "models")),
		ProvidersDir:        CleanOrEmpty(filepath.Join(paths.RegistriesDir, "providers")),
		ConnectorsCenterDir: CleanOrEmpty(paths.EffectiveConnectorsCenterDir()),
		ConnectorsDir:       AgentConnectorPath(agentDir),
		ViewportServersDir:  CleanOrEmpty(filepath.Join(paths.RegistriesDir, "viewport-servers")),
		ToolsDir:            CleanOrEmpty(paths.ToolsDir),
		ViewportsDir:        CleanOrEmpty(filepath.Join(filepath.Dir(filepath.Clean(paths.RegistriesDir)), "viewports")),
	}, nil
}

func ValidateWorkspaceChatsSeparation(workspaceRoot string, chatsRoot string) error {
	if strings.TrimSpace(workspaceRoot) == "" || strings.TrimSpace(chatsRoot) == "" {
		return nil
	}
	if _, err := rootpaths.New(workspaceRoot, chatsRoot, ""); err != nil {
		return fmt.Errorf("workspace/chats validation failed: %w", err)
	}
	return nil
}

func ResolveHostWorkspaceRoot(workspaceRoot string) (string, error) {
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

func EnsureChatDir(paths config.PathsConfig, chatID string) (string, error) {
	dir := ChatDirPath(paths, chatID)
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

func ChatDirPath(paths config.PathsConfig, chatID string) string {
	chatID = strings.TrimSpace(chatID)
	chatsDir := strings.TrimSpace(paths.ChatsDir)
	if chatID == "" || chatsDir == "" {
		return ""
	}
	return AbsOrEmpty(filepath.Join(chatsDir, chatID))
}

func ResolveSandboxPaths(cfg config.Config, def catalog.AgentDefinition, localPaths contracts.LocalPaths) contracts.SandboxPaths {
	if cfg.IsLocalMode() {
		return ResolveLocalSandboxPaths(cfg, def, localPaths)
	}
	return ResolveContainerSandboxPaths(cfg, def)
}

func ResolveContainerSandboxPaths(cfg config.Config, def catalog.AgentDefinition) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(AnyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := def.RuntimeDir != ""
	hasSkillsDir := level != "global" && hasAgentDir

	var skillsCenterDir string
	ownerDir := IfNonEmpty(cfg.Paths.OwnerDir, "/owner")
	var ruAgentsDir string
	var teamsDir string
	var automationsDir string
	var chatsDir string
	memoryDir := IfNonEmpty(cfg.Paths.MemoryDir, "/memory")
	var modelsDir string
	var providersDir string
	var connectorsDir string
	var connectorsCenterDir string
	if len(def.ConnectorMounts) > 0 {
		connectorsDir = "/connectors"
	}
	var viewportServersDir string
	var toolsDir string
	var viewportsDir string
	for _, mount := range PromptContextSandboxMounts(def.Runtime["sandboxMounts"]) {
		switch strings.ToLower(strings.TrimSpace(AnyString(mount["platform"]))) {
		case "skills-center":
			skillsCenterDir = "/skills-center"
		case "agents":
			ruAgentsDir = "/agents"
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
		case "connectors":
			connectorsDir = "/connectors"
		case "connectors-center":
			connectorsCenterDir = "/connectors-center"
		case "viewport-servers":
			viewportServersDir = "/viewport-servers"
		case "tools":
			toolsDir = "/tools"
		case "viewports":
			viewportsDir = "/viewports"
		}
	}

	return contracts.SandboxPaths{
		WorkspaceDir:        "/workspace",
		ChatDir:             "/chat",
		RootDir:             IfNonEmpty(cfg.Paths.RootDir, "/root"),
		SkillsDir:           BoolPath(hasSkillsDir, "/skills"),
		SkillsCenterDir:     skillsCenterDir,
		PanDir:              IfNonEmpty(cfg.Paths.PanDir, "/pan"),
		AgentDir:            BoolPath(hasAgentDir, "/agent"),
		OwnerDir:            ownerDir,
		RUAgentsDir:         ruAgentsDir,
		TeamsDir:            teamsDir,
		AutomationsDir:      automationsDir,
		ChatsDir:            chatsDir,
		MemoryDir:           memoryDir,
		ModelsDir:           modelsDir,
		ProvidersDir:        providersDir,
		ConnectorsDir:       connectorsDir,
		ConnectorsCenterDir: connectorsCenterDir,
		ViewportServersDir:  viewportServersDir,
		ToolsDir:            toolsDir,
		ViewportsDir:        viewportsDir,
	}
}

func ResolveLocalSandboxPaths(cfg config.Config, def catalog.AgentDefinition, localPaths contracts.LocalPaths) contracts.SandboxPaths {
	level := strings.ToLower(strings.TrimSpace(AnyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "run"
	}
	hasAgentDir := strings.TrimSpace(def.RuntimeDir) != ""
	hasSkillsDir := level != "global" && hasAgentDir

	paths := contracts.SandboxPaths{
		WorkspaceDir: localPaths.WorkspaceDir,
		ChatDir:      localPaths.ChatDir,
		RootDir:      AbsOrEmpty(cfg.Paths.RootDir),
		SkillsDir:    ResolveLocalSkillsDir(hasSkillsDir, level, def.RuntimeDir),
		PanDir:       AbsOrEmpty(cfg.Paths.PanDir),
		AgentDir:     AbsOrEmpty(def.RuntimeDir),
		OwnerDir:     AbsOrEmpty(cfg.Paths.OwnerDir),
		MemoryDir:    AbsOrEmpty(cfg.Paths.MemoryDir),
	}
	for _, mount := range PromptContextSandboxMounts(def.Runtime["sandboxMounts"]) {
		switch strings.ToLower(strings.TrimSpace(AnyString(mount["platform"]))) {
		case "skills-center":
			paths.SkillsCenterDir = AbsOrEmpty(cfg.Paths.SkillsCenterDir)
		case "agents":
			paths.RUAgentsDir = AbsOrEmpty(cfg.Paths.EffectiveRUAgentsDir())
		case "teams":
			paths.TeamsDir = AbsOrEmpty(cfg.Paths.TeamsDir)
		case "automations":
			paths.AutomationsDir = AbsOrEmpty(cfg.Paths.AutomationsDir)
		case "chats":
			paths.ChatsDir = AbsOrEmpty(cfg.Paths.ChatsDir)
		case "models":
			paths.ModelsDir = AbsOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "models"))
		case "providers":
			paths.ProvidersDir = AbsOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "providers"))
		case "connectors":
			paths.ConnectorsDir = AgentConnectorPath(def.RuntimeDir)
		case "connectors-center":
			paths.ConnectorsCenterDir = AbsOrEmpty(cfg.Paths.EffectiveConnectorsCenterDir())
		case "viewport-servers":
			paths.ViewportServersDir = AbsOrEmpty(filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers"))
		case "tools":
			paths.ToolsDir = AbsOrEmpty(cfg.Paths.ToolsDir)
		case "viewports":
			paths.ViewportsDir = AbsOrEmpty(filepath.Join(filepath.Dir(filepath.Clean(cfg.Paths.RegistriesDir)), "viewports"))
		}
	}
	if len(def.ConnectorMounts) > 0 {
		paths.ConnectorsDir = AgentConnectorPath(def.RuntimeDir)
	}
	return paths
}

func BuildAgentDigests(registry Catalog) []contracts.AgentDigest {
	if registry == nil {
		return nil
	}
	items := registry.AgentDigests()
	digests := make([]contracts.AgentDigest, 0, len(items))
	for _, item := range items {
		def, ok := registry.AgentDefinition(item.Key)
		if !ok {
			// A registry may expose a summary while concurrently reloading its
			// definition. Keep the digest useful from the public summary instead
			// of relying on the list-only meta payload.
			def = catalog.AgentDefinition{Mode: item.Mode}
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
		environmentID := strings.TrimSpace(AnyString(def.Runtime["environmentId"]))
		level := strings.TrimSpace(AnyString(def.Runtime["level"]))
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

func BuildContextAgentDigests(registry Catalog, def catalog.AgentDefinition, currentAgentKey string) ([]contracts.AgentDigest, *catalog.AdminAgentDiagnostic) {
	if !AgentHasContextTag(def, "agents") {
		return nil, nil
	}
	digests := BuildAgentDigests(registry)
	keys := make([]string, 0, len(digests))
	byKey := make(map[string]contracts.AgentDigest, len(digests))
	for _, digest := range digests {
		key := strings.TrimSpace(digest.Key)
		if key != "" {
			keys = append(keys, key)
			byKey[key] = digest
		}
	}
	selected, diagnostic := catalog.ResolveContextAgentKeys(def, currentAgentKey, keys)
	filtered := make([]contracts.AgentDigest, 0, len(selected))
	for _, key := range selected {
		filtered = append(filtered, byKey[key])
	}
	return filtered, diagnostic
}

func AgentHasContextTag(def catalog.AgentDefinition, tag string) bool {
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

func BuildAuthIdentity(principal *contracts.AuthIdentity) *contracts.AuthIdentity {
	if principal == nil {
		return nil
	}
	identity := *principal
	return &identity
}

func BuildSandboxContext(cfg config.Config, def catalog.AgentDefinition) (*contracts.SandboxContext, error) {
	configuredEnvironmentID := strings.TrimSpace(AnyString(def.Runtime["environmentId"]))
	defaultEnvironmentID := strings.TrimSpace(cfg.ContainerHub.DefaultEnvironmentID)
	environmentID := configuredEnvironmentID
	if environmentID == "" {
		environmentID = defaultEnvironmentID
	}
	if environmentID == "" {
		return nil, fmt.Errorf("sandbox context requires a non-blank environmentId")
	}

	level := strings.ToUpper(strings.TrimSpace(AnyString(def.Runtime["level"])))
	if level == "" {
		level = strings.ToUpper(strings.TrimSpace(cfg.ContainerHub.DefaultSandboxLevel))
	}
	if level == "" {
		level = "RUN"
	}

	prompt, err := FetchSandboxPrompt(cfg.ContainerHub, environmentID)
	if err != nil {
		return nil, err
	}
	return &contracts.SandboxContext{
		EnvironmentID:           environmentID,
		ConfiguredEnvironmentID: configuredEnvironmentID,
		DefaultEnvironmentID:    defaultEnvironmentID,
		Level:                   level,
		ContainerHubEnabled:     cfg.ContainerHub.Enabled,
		UsesSandboxBash:         HasRuntimeSandbox(def.Runtime),
		ExtraMounts:             SummarizeSandboxMounts(def),
		EnvironmentPrompt:       prompt,
	}, nil
}

func FetchSandboxPrompt(cfg config.ContainerHubConfig, environmentID string) (string, error) {
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

func SummarizeSandboxMounts(def catalog.AgentDefinition) []string {
	mounts := PromptContextSandboxMounts(def.Runtime["sandboxMounts"])
	out := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mode := strings.ToLower(strings.TrimSpace(AnyString(mount["mode"])))
		if mode == "" {
			mode = "unspecified"
		}
		platform := strings.TrimSpace(AnyString(mount["platform"]))
		source := strings.TrimSpace(AnyString(mount["source"]))
		destination := strings.TrimSpace(AnyString(mount["destination"]))
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

func FirstStringClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(AnyString(claims[key])); value != "" {
			return value
		}
	}
	return ""
}

func PromptContextSandboxMounts(value any) []map[string]any {
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

func AnyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func CleanOrEmpty(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}

func AbsOrEmpty(path string) string {
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

func ResolveLocalSkillsDir(hasSkillsDir bool, level string, agentDir string) string {
	if !hasSkillsDir {
		return ""
	}
	if level != "global" && strings.TrimSpace(agentDir) != "" {
		return AbsOrEmpty(filepath.Join(agentDir, "skills"))
	}
	return ""
}

func PromptContextHasPlatformMount(sandboxMounts any, platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return false
	}
	for _, mount := range PromptContextSandboxMounts(sandboxMounts) {
		if strings.EqualFold(strings.TrimSpace(AnyString(mount["platform"])), platform) {
			return true
		}
	}
	return false
}

func IfNonEmpty(path string, target string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return target
}

func BoolPath(ok bool, target string) string {
	if !ok {
		return ""
	}
	return target
}

func ContainsString(items []string, needle string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == strings.TrimSpace(needle) {
			return true
		}
	}
	return false
}

func AgentConnectorPath(agentDir string) string {
	if strings.TrimSpace(agentDir) == "" {
		return ""
	}
	return "@connectors"
}
