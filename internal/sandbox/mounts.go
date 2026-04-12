package sandbox

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
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

func (r *ContainerHubMountResolver) Resolve(chatID string, agentKey string, level string, extraMounts []contracts.SandboxExtraMount) ([]MountSpec, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return nil, fmt.Errorf("container-hub mount validation failed for agent-self: agentKey is required")
	}
	workspaceRoot, err := hostPath("CHATS_DIR", r.paths.ChatsDir)
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
	if panDir, err := hostPath("PAN_DIR", r.paths.PanDir); err == nil && panDir != "" {
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
	if err := r.applyExtraMounts(&mounts, agentKey, extraMounts); err != nil {
		return nil, err
	}

	return mounts, nil
}

func (r *ContainerHubMountResolver) applyExtraMounts(mounts *[]MountSpec, agentKey string, extraMounts []contracts.SandboxExtraMount) error {
	for _, extraMount := range extraMounts {
		if isZeroExtraMount(extraMount) {
			continue
		}
		destination := normalizeContainerPath(extraMount.Destination)
		if isDefaultMountOverride(extraMount, destination) {
			readOnly, err := parseMountMode(extraMount.Mode, "default-mount-override", destination)
			if err != nil {
				return err
			}
			if err := applyMountOverride(mounts, destination, readOnly); err != nil {
				return err
			}
			continue
		}
		if strings.TrimSpace(extraMount.Platform) != "" {
			if err := r.resolvePlatformMount(mounts, agentKey, extraMount); err != nil {
				return err
			}
			continue
		}
		if err := r.resolveCustomMount(mounts, extraMount, destination); err != nil {
			return err
		}
	}
	return nil
}

func isZeroExtraMount(extraMount contracts.SandboxExtraMount) bool {
	return strings.TrimSpace(extraMount.Platform) == "" &&
		strings.TrimSpace(extraMount.Source) == "" &&
		strings.TrimSpace(extraMount.Destination) == "" &&
		strings.TrimSpace(extraMount.Mode) == ""
}

func isDefaultMountOverride(extraMount contracts.SandboxExtraMount, destination string) bool {
	return strings.TrimSpace(extraMount.Platform) == "" &&
		strings.TrimSpace(extraMount.Source) == "" &&
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

func (r *ContainerHubMountResolver) resolvePlatformMount(mounts *[]MountSpec, agentKey string, extraMount contracts.SandboxExtraMount) error {
	platform := strings.ToLower(strings.TrimSpace(extraMount.Platform))
	def, ok := r.platformMountDef(platform, agentKey)
	if !ok {
		log.Printf("[container-hub] skip unknown sandboxConfig.extraMounts platform %q", extraMount.Platform)
		return nil
	}
	readOnly, err := parseMountMode(extraMount.Mode, "extra-mount:"+platform, def.destination)
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
		return fmt.Errorf("container-hub mount validation failed for extra-mount:%s: source is not configured (containerPath=%s)", platform, def.destination)
	}
	if err := validateMountDirectory("extra-mount:"+platform, source, def.destination); err != nil {
		return err
	}
	return appendMount(mounts, MountSpec{
		Name:        "extra-mount:" + platform,
		Source:      source,
		Destination: def.destination,
		ReadOnly:    readOnly,
	})
}

func (r *ContainerHubMountResolver) resolveCustomMount(mounts *[]MountSpec, extraMount contracts.SandboxExtraMount, destination string) error {
	readOnly, err := parseMountMode(extraMount.Mode, "extra-mount", destination)
	if err != nil {
		return err
	}
	if destination != "" && isDefaultMountDestination(destination) {
		return fmt.Errorf("container-hub mount validation failed for extra-mount: overriding a default mount must omit source/platform and only declare destination + mode (destination=%s)", destination)
	}
	source := strings.TrimSpace(extraMount.Source)
	if source == "" || destination == "" {
		return fmt.Errorf("container-hub mount validation failed for extra-mount: custom mount requires source + destination + mode")
	}
	if !strings.HasPrefix(destination, "/") {
		return fmt.Errorf("container-hub mount validation failed for extra-mount: destination must be an absolute path (destination=%s)", extraMount.Destination)
	}
	source = filepath.Clean(source)
	if err := validateMountDirectory("extra-mount", source, destination); err != nil {
		return err
	}
	return appendMount(mounts, MountSpec{
		Name:        "extra-mount",
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
		"chats":         {destination: "/chats", source: func() (string, error) { return hostPath("CHATS_DIR", r.paths.ChatsDir) }},
		"memory":        {destination: "/memory", overrideOnly: true},
		"mcp-servers":   {destination: "/mcp-servers", source: func() (string, error) { return r.registryChildSource("mcp-servers") }},
		"models":        {destination: "/models", source: func() (string, error) { return r.registryChildSource("models") }},
		"owner":         {destination: "/owner", overrideOnly: true},
		"providers":     {destination: "/providers", source: func() (string, error) { return r.registryChildSource("providers") }},
		"schedules":     {destination: "/schedules", source: func() (string, error) { return hostPath("SCHEDULES_DIR", r.paths.SchedulesDir) }},
		"skills-market": {destination: "/skills-market", source: func() (string, error) { return hostPath("SKILLS_MARKET_DIR", r.paths.SkillsMarketDir) }},
		"teams":         {destination: "/teams", source: func() (string, error) { return hostPath("TEAMS_DIR", r.paths.TeamsDir) }},
		"tools":         {destination: "/tools", source: func() (string, error) { return r.registryChildSource("tools") }},
	}
	def, ok := defs[platform]
	return def, ok
}

func (r *ContainerHubMountResolver) registryChildSource(child string) (string, error) {
	registriesRoot, err := hostPath("REGISTRIES_DIR", r.paths.RegistriesDir)
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
		return hostPath("SKILLS_MARKET_DIR", r.paths.SkillsMarketDir)
	}
	agentDir, err := hostPath("AGENTS_DIR", r.paths.AgentsDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for skills-dir: %w", err)
	}
	if agentKey != "" {
		localSkills := filepath.Join(agentDir, agentKey, "skills")
		if err := os.MkdirAll(localSkills, 0o755); err == nil {
			return localSkills, nil
		}
	}
	return hostPath("SKILLS_MARKET_DIR", r.paths.SkillsMarketDir)
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
	memoryRoot, err := hostPath("MEMORY_DIR", r.paths.MemoryDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for memory-dir: %w", err)
	}
	if memoryRoot == "" {
		return "", fmt.Errorf("container-hub mount validation failed for memory-dir: MEMORY_DIR is required")
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
	hostValue := strings.TrimSpace(os.Getenv(envKey))
	if hostValue == "" {
		hostValue = configured
	}
	if strings.HasPrefix(filepath.Clean(hostValue), "/opt/") {
		return "", fmt.Errorf("missing %s host path (configured=%s)", envKey, configured)
	}
	return filepath.Clean(hostValue), nil
}
