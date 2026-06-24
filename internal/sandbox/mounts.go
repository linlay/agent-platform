package sandbox

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type ContainerHubMountResolver struct {
	paths config.PathsConfig
}

type MountSpec struct {
	Name        string
	Source      string
	Destination string
	ReadOnly    bool
}

func NewContainerHubMountResolver(paths config.PathsConfig) *ContainerHubMountResolver {
	return &ContainerHubMountResolver{paths: paths}
}

func (r *ContainerHubMountResolver) Resolve(chatID string, agentKey string, level string, sandboxMounts []contracts.SandboxExtraMount) ([]MountSpec, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return nil, fmt.Errorf("container-hub mount validation failed for agent-self: agentKey is required")
	}
	workspaceRoot, err := hostPath("AP_RUNTIME_CHATS_DIR", r.paths.ChatsDir)
	if err != nil {
		return nil, fmt.Errorf("container-hub mount validation failed for data-dir: %w", err)
	}
	workspaceSource := workspaceRoot
	if chatID != "" {
		workspaceSource = filepath.Join(workspaceRoot, chatID)
	}
	if err := os.MkdirAll(workspaceSource, 0o755); err != nil {
		return nil, err
	}

	mounts := []MountSpec{
		{Name: "data-dir", Source: workspaceSource, Destination: "/workspace", ReadOnly: false},
	}

	if rootDir, err := hostPath("ROOT_DIR", r.paths.RootDir); err == nil && rootDir != "" {
		mounts = append(mounts, MountSpec{Name: "root-dir", Source: rootDir, Destination: "/root", ReadOnly: false})
	} else if err != nil {
		return nil, fmt.Errorf("container-hub mount validation failed for root-dir: %w", err)
	}
	if panDir, err := hostPath("AP_RUNTIME_PAN_DIR", r.paths.PanDir); err == nil && panDir != "" {
		mounts = append(mounts, MountSpec{Name: "pan-dir", Source: panDir, Destination: "/pan", ReadOnly: false})
	} else if err != nil {
		return nil, fmt.Errorf("container-hub mount validation failed for pan-dir: %w", err)
	}
	if agentDir, err := r.agentSource(agentKey); err == nil && agentDir != "" {
		mounts = append(mounts, MountSpec{Name: "agent-self", Source: agentDir, Destination: "/agent", ReadOnly: true})
	} else if err != nil {
		return nil, err
	}

	skillsSource, err := r.skillsSource(agentKey, level)
	if err != nil {
		return nil, err
	}
	if skillsSource != "" {
		mounts = append(mounts, MountSpec{Name: "skills-dir", Source: skillsSource, Destination: "/skills", ReadOnly: true})
	}
	if ownerDir, err := r.ownerSource(); err == nil && ownerDir != "" {
		mounts = append(mounts, MountSpec{Name: "owner-dir", Source: ownerDir, Destination: "/owner", ReadOnly: true})
	} else if err != nil {
		return nil, err
	}
	if memoryDir, err := r.memorySource(agentKey); err == nil && memoryDir != "" {
		mounts = append(mounts, MountSpec{Name: "memory-dir", Source: memoryDir, Destination: "/memory", ReadOnly: true})
	} else if err != nil {
		return nil, err
	}
	if err := r.applySandboxMounts(&mounts, agentKey, sandboxMounts); err != nil {
		return nil, err
	}

	return mounts, nil
}

func (r *ContainerHubMountResolver) applySandboxMounts(mounts *[]MountSpec, agentKey string, sandboxMounts []contracts.SandboxExtraMount) error {
	for _, sandboxMount := range sandboxMounts {
		if isZeroSandboxMount(sandboxMount) {
			continue
		}
		destination := normalizeContainerPath(sandboxMount.Destination)
		if isDefaultMountOverride(sandboxMount, destination) {
			readOnly, err := parseMountMode(sandboxMount.Mode, "default-mount-override", destination)
			if err != nil {
				return err
			}
			if err := applyMountOverride(mounts, destination, readOnly); err != nil {
				return err
			}
			continue
		}
		if strings.TrimSpace(sandboxMount.Platform) != "" {
			if err := r.resolvePlatformMount(mounts, agentKey, sandboxMount); err != nil {
				return err
			}
			continue
		}
		if err := r.resolveCustomMount(mounts, sandboxMount, destination); err != nil {
			return err
		}
	}
	return nil
}

func isZeroSandboxMount(sandboxMount contracts.SandboxExtraMount) bool {
	return strings.TrimSpace(sandboxMount.Platform) == "" &&
		strings.TrimSpace(sandboxMount.Source) == "" &&
		strings.TrimSpace(sandboxMount.Destination) == "" &&
		strings.TrimSpace(sandboxMount.Mode) == ""
}

func isDefaultMountOverride(sandboxMount contracts.SandboxExtraMount, destination string) bool {
	return strings.TrimSpace(sandboxMount.Platform) == "" &&
		strings.TrimSpace(sandboxMount.Source) == "" &&
		destination != "" &&
		isDefaultMountDestination(destination)
}

func parseMountMode(mode string, mountName string, destination string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ro":
		return true, nil
	case "rw":
		return false, nil
	default:
		if destination != "" {
			return false, fmt.Errorf("container-hub mount validation failed for %s: mode is required (destination=%s)", mountName, destination)
		}
		return false, fmt.Errorf("container-hub mount validation failed for %s: mode is required", mountName)
	}
}

func applyMountOverride(mounts *[]MountSpec, destination string, readOnly bool) error {
	index := findMountIndex(*mounts, destination)
	if index < 0 {
		return fmt.Errorf("container-hub mount validation failed for default-mount-override: default mount is not available (destination=%s)", destination)
	}
	(*mounts)[index].ReadOnly = readOnly
	return nil
}

func (r *ContainerHubMountResolver) resolvePlatformMount(mounts *[]MountSpec, agentKey string, sandboxMount contracts.SandboxExtraMount) error {
	platform := strings.ToLower(strings.TrimSpace(sandboxMount.Platform))
	def, ok := r.platformMountDef(platform, agentKey)
	if !ok {
		log.Printf("[container-hub] skip unknown runtimeConfig.sandboxMounts platform %q", sandboxMount.Platform)
		return nil
	}
	readOnly, err := parseMountMode(sandboxMount.Mode, "sandbox-mount:"+platform, def.destination)
	if err != nil {
		return err
	}
	if def.overrideOnly {
		return applyMountOverride(mounts, def.destination, readOnly)
	}
	source, err := def.source()
	if err != nil {
		return err
	}
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("container-hub mount validation failed for sandbox-mount:%s: source is not configured (containerPath=%s)", platform, def.destination)
	}
	if err := validateMountDirectory("sandbox-mount:"+platform, source, def.destination); err != nil {
		return err
	}
	return appendMount(mounts, MountSpec{
		Name:        "sandbox-mount:" + platform,
		Source:      source,
		Destination: def.destination,
		ReadOnly:    readOnly,
	})
}

func (r *ContainerHubMountResolver) resolveCustomMount(mounts *[]MountSpec, sandboxMount contracts.SandboxExtraMount, destination string) error {
	readOnly, err := parseMountMode(sandboxMount.Mode, "sandbox-mount", destination)
	if err != nil {
		return err
	}
	if destination != "" && isDefaultMountDestination(destination) {
		return fmt.Errorf("container-hub mount validation failed for sandbox-mount: overriding a default mount must omit source/platform and only declare destination + mode (destination=%s)", destination)
	}
	source := strings.TrimSpace(sandboxMount.Source)
	if source == "" || destination == "" {
		return fmt.Errorf("container-hub mount validation failed for sandbox-mount: custom mount requires source + destination + mode")
	}
	if !strings.HasPrefix(destination, "/") {
		return fmt.Errorf("container-hub mount validation failed for sandbox-mount: destination must be an absolute path (destination=%s)", sandboxMount.Destination)
	}
	source = filepath.Clean(source)
	if err := validateMountDirectory("sandbox-mount", source, destination); err != nil {
		return err
	}
	return appendMount(mounts, MountSpec{
		Name:        "sandbox-mount",
		Source:      source,
		Destination: destination,
		ReadOnly:    readOnly,
	})
}

type platformMountDefinition struct {
	destination  string
	source       func() (string, error)
	overrideOnly bool
}

func (r *ContainerHubMountResolver) platformMountDef(platform string, agentKey string) (platformMountDefinition, bool) {
	defs := map[string]platformMountDefinition{
		"agent":         {destination: "/agent", overrideOnly: true},
		"agents":        {destination: "/agents", source: func() (string, error) { return hostPath("AGENTS_DIR", r.paths.AgentsDir) }},
		"chats":         {destination: "/chats", source: func() (string, error) { return hostPath("AP_RUNTIME_CHATS_DIR", r.paths.ChatsDir) }},
		"memory":        {destination: "/memory", overrideOnly: true},
		"mcp-servers":   {destination: "/mcp-servers", source: func() (string, error) { return r.registryChildSource("mcp-servers") }},
		"models":        {destination: "/models", source: func() (string, error) { return r.registryChildSource("models") }},
		"owner":         {destination: "/owner", overrideOnly: true},
		"providers":     {destination: "/providers", source: func() (string, error) { return r.registryChildSource("providers") }},
		"automations":   {destination: "/automations", source: func() (string, error) { return hostPath("AUTOMATIONS_DIR", r.paths.AutomationsDir) }},
		"skills-market": {destination: "/skills-market", source: func() (string, error) { return hostPath("SKILLS_MARKET_DIR", r.paths.SkillsMarketDir) }},
		"teams":         {destination: "/teams", source: func() (string, error) { return hostPath("TEAMS_DIR", r.paths.TeamsDir) }},
		"tools":         {destination: "/tools", source: func() (string, error) { return r.registryChildSource("tools") }},
	}
	def, ok := defs[platform]
	return def, ok
}

func (r *ContainerHubMountResolver) registryChildSource(child string) (string, error) {
	registriesRoot, err := hostPath("AP_RUNTIME_REGISTRIES_DIR", r.paths.RegistriesDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for %s-dir: %w", child, err)
	}
	if strings.TrimSpace(registriesRoot) == "" {
		return "", nil
	}
	return filepath.Join(registriesRoot, child), nil
}

func normalizeContainerPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(trimmed))
}

func isDefaultMountDestination(destination string) bool {
	switch destination {
	case "/workspace", "/root", "/skills", "/pan", "/agent", "/owner", "/memory":
		return true
	default:
		return false
	}
}

func appendMount(mounts *[]MountSpec, mount MountSpec) error {
	if index := findMountIndex(*mounts, mount.Destination); index >= 0 {
		return fmt.Errorf("container-hub mount validation failed for %s: containerPath conflicts with existing mount (containerPath=%s)", mount.Name, mount.Destination)
	}
	*mounts = append(*mounts, mount)
	return nil
}

func findMountIndex(mounts []MountSpec, destination string) int {
	for i, mount := range mounts {
		if mount.Destination == destination {
			return i
		}
	}
	return -1
}

func validateMountDirectory(mountName string, source string, destination string) error {
	stat, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("container-hub mount validation failed for %s: source does not exist (resolved=%s, containerPath=%s)", mountName, source, destination)
		}
		return fmt.Errorf("container-hub mount validation failed for %s: %w", mountName, err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("container-hub mount validation failed for %s: source is not a directory (resolved=%s, containerPath=%s)", mountName, source, destination)
	}
	return nil
}

func (r *ContainerHubMountResolver) skillsSource(agentKey string, level string) (string, error) {
	if strings.EqualFold(level, "global") {
		return "", nil
	}
	agentDir, err := hostPath("AGENTS_DIR", r.paths.AgentsDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for skills-dir: %w", err)
	}
	if agentKey != "" {
		localSkills := filepath.Join(agentDir, agentKey, "skills")
		if err := os.MkdirAll(localSkills, 0o755); err != nil {
			return "", fmt.Errorf("container-hub mount validation failed for skills-dir: %w", err)
		}
		return localSkills, nil
	}
	return "", nil
}

func (r *ContainerHubMountResolver) agentSource(agentKey string) (string, error) {
	agentsRoot, err := hostPath("AGENTS_DIR", r.paths.AgentsDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for agent-self: %w", err)
	}
	if agentsRoot == "" {
		return "", fmt.Errorf("container-hub mount validation failed for agent-self: AGENTS_DIR is required")
	}
	agentDir := filepath.Join(agentsRoot, agentKey)
	if stat, err := os.Stat(agentDir); err == nil && stat.IsDir() {
		return agentDir, nil
	}
	return "", fmt.Errorf("container-hub mount validation failed for agent-self: missing agent directory %s", agentDir)
}

func (r *ContainerHubMountResolver) ownerSource() (string, error) {
	ownerDir, err := hostPath("OWNER_DIR", r.paths.OwnerDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for owner-dir: %w", err)
	}
	if ownerDir == "" {
		return "", fmt.Errorf("container-hub mount validation failed for owner-dir: OWNER_DIR is required")
	}
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for owner-dir: %w", err)
	}
	return ownerDir, nil
}

func (r *ContainerHubMountResolver) memorySource(agentKey string) (string, error) {
	memoryRoot, err := hostPath("AP_RUNTIME_MEMORY_DIR", r.paths.MemoryDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for memory-dir: %w", err)
	}
	if memoryRoot == "" {
		return "", fmt.Errorf("container-hub mount validation failed for memory-dir: AP_RUNTIME_MEMORY_DIR is required")
	}
	memoryDir := filepath.Join(memoryRoot, agentKey)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for memory-dir: %w", err)
	}
	return memoryDir, nil
}

func hostPath(envKey string, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", nil
	}
	hostValue := ""
	if allowHostPathEnv(envKey) {
		hostValue = strings.TrimSpace(os.Getenv(envKey))
	}
	if hostValue == "" {
		hostValue = configured
	}
	if strings.HasPrefix(filepath.Clean(hostValue), "/opt/") {
		return "", fmt.Errorf("missing %s host path (configured=%s)", envKey, configured)
	}
	return filepath.Clean(hostValue), nil
}

func allowHostPathEnv(envKey string) bool {
	switch envKey {
	case "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_PAN_DIR", "AP_RUNTIME_REGISTRIES_DIR":
		return true
	default:
		return false
	}
}
