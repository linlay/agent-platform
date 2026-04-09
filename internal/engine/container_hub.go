package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/config"
)

type ContainerHubClient struct {
	baseURL    string
	authToken  string
	timeout    time.Duration
	httpClient *http.Client
}

type EnvironmentAgentPromptResult struct {
	EnvironmentName string
	HasPrompt       bool
	Prompt          string
	UpdatedAt       string
	Error           string
	OK              bool
}

func NewContainerHubClient(cfg config.ContainerHubConfig) *ContainerHubClient {
	return &ContainerHubClient{
		baseURL:   strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		authToken: strings.TrimSpace(cfg.AuthToken),
		timeout:   time.Duration(maxInt(cfg.RequestTimeoutMs, 1)) * time.Millisecond,
		httpClient: &http.Client{
			Timeout: time.Duration(maxInt(cfg.RequestTimeoutMs, 1)) * time.Millisecond,
		},
	}
}

func (c *ContainerHubClient) CreateSession(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return c.post(ctx, "/api/sessions/create", payload)
}

// ExecuteSessionRaw calls the container-hub execute API and returns the raw response.
// Container Hub returns plain text on success, JSON error on failure.
// Java: ContainerHubClient.executeSession with contentTypeAware=true
//
//	→ success: textBody(response.body()) returns raw text as-is
//	→ failure: parsed as JSON error
func (c *ContainerHubClient) ExecuteSessionRaw(ctx context.Context, sessionID string, payload map[string]any) (string, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/sessions/"+strings.TrimSpace(sessionID)+"/execute", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("/api/sessions/execute returned status %d", resp.StatusCode)
	}
	// Return raw text + whether it's JSON (error) or plain text (success)
	text := string(rawBody)
	var jsonCheck map[string]any
	isJSON := json.Unmarshal(rawBody, &jsonCheck) == nil
	return text, isJSON, nil
}

func (c *ContainerHubClient) StopSession(ctx context.Context, sessionID string) (map[string]any, error) {
	return c.post(ctx, "/api/sessions/"+strings.TrimSpace(sessionID)+"/stop", map[string]any{})
}

func (c *ContainerHubClient) GetEnvironmentAgentPrompt(environmentID string) (EnvironmentAgentPromptResult, error) {
	normalized := strings.TrimSpace(environmentID)
	if normalized == "" {
		return EnvironmentAgentPromptResult{}, fmt.Errorf("environment id is required")
	}
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/environments/"+normalized+"/agent-prompt", nil)
	if err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	result := EnvironmentAgentPromptResult{
		EnvironmentName: strings.TrimSpace(firstStringValue(decoded, "environmentName", "environment_name")),
		HasPrompt:       firstBoolValue(decoded, "hasPrompt", "has_prompt"),
		Prompt:          firstStringValue(decoded, "prompt"),
		UpdatedAt:       firstStringValue(decoded, "updatedAt", "updated_at"),
		OK:              resp.StatusCode >= 200 && resp.StatusCode < 300,
	}
	if !result.OK {
		result.Error = firstStringValue(decoded, "error")
		if strings.TrimSpace(result.Error) == "" {
			result.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
	}
	return result, nil
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(anyStringNode(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func firstBoolValue(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return anyBoolNode(value)
		}
	}
	return false
}

func (c *ContainerHubClient) post(ctx context.Context, path string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := json.Marshal(decoded)
		log.Printf("[container-hub] %s %d request=%s response=%s", path, resp.StatusCode, string(body), string(detail))
		return nil, fmt.Errorf("%s returned status %d", path, resp.StatusCode)
	}
	return decoded, nil
}

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

func (r *ContainerHubMountResolver) Resolve(chatID string, agentKey string, level string, extraMounts []SandboxExtraMount) ([]MountSpec, error) {
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

func (r *ContainerHubMountResolver) applyExtraMounts(mounts *[]MountSpec, agentKey string, extraMounts []SandboxExtraMount) error {
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

func isZeroExtraMount(extraMount SandboxExtraMount) bool {
	return strings.TrimSpace(extraMount.Platform) == "" &&
		strings.TrimSpace(extraMount.Source) == "" &&
		strings.TrimSpace(extraMount.Destination) == "" &&
		strings.TrimSpace(extraMount.Mode) == ""
}

func isDefaultMountOverride(extraMount SandboxExtraMount, destination string) bool {
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

func (r *ContainerHubMountResolver) resolvePlatformMount(mounts *[]MountSpec, agentKey string, extraMount SandboxExtraMount) error {
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

func (r *ContainerHubMountResolver) resolveCustomMount(mounts *[]MountSpec, extraMount SandboxExtraMount, destination string) error {
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

type ContainerHubSandboxService struct {
	cfg           config.ContainerHubConfig
	client        *ContainerHubClient
	mounts        *ContainerHubMountResolver
	mu            sync.Mutex
	agentSessions map[string]*managedSandboxSession
	globalSession *managedSandboxSession
}

type managedSandboxSession struct {
	session     *SandboxSession
	activeUsers int
	lastUsed    time.Time
}

func NewContainerHubSandboxService(cfg config.ContainerHubConfig, paths config.PathsConfig) *ContainerHubSandboxService {
	return &ContainerHubSandboxService{
		cfg:           cfg,
		client:        NewContainerHubClient(cfg),
		mounts:        NewContainerHubMountResolver(paths),
		agentSessions: map[string]*managedSandboxSession{},
	}
}

func (s *ContainerHubSandboxService) OpenIfNeeded(ctx context.Context, execCtx *ExecutionContext) error {
	if execCtx == nil {
		return fmt.Errorf("missing execution context")
	}
	if execCtx.SandboxSession != nil {
		return nil
	}
	if !s.cfg.Enabled {
		return fmt.Errorf("container-hub sandbox is disabled")
	}

	// Resolve environmentId: agent sandboxConfig > global default (mirrors Java)
	environmentID := s.resolveEnvironmentID(execCtx)
	if environmentID == "" {
		return fmt.Errorf("container-hub environment id is required")
	}
	// Store resolved ID so acquire methods can use it
	execCtx.resolvedEnvironmentID = environmentID

	level := s.resolveSandboxLevel(execCtx)
	if level == "" {
		level = "run"
	}

	switch level {
	case "agent":
		return s.acquireAgentSession(ctx, execCtx)
	case "global":
		return s.acquireGlobalSession(ctx, execCtx)
	default:
		return s.acquireRunSession(ctx, execCtx)
	}
}

// resolveEnvironmentID mirrors Java's ContainerHubSandboxService.resolveEnvironmentId:
// agent sandboxConfig.environmentId > global default.
func (s *ContainerHubSandboxService) resolveEnvironmentID(execCtx *ExecutionContext) string {
	if execCtx != nil && execCtx.Session.SandboxEnvironmentID != "" {
		return strings.TrimSpace(execCtx.Session.SandboxEnvironmentID)
	}
	return strings.TrimSpace(s.cfg.DefaultEnvironmentID)
}

func (s *ContainerHubSandboxService) resolveSandboxLevel(execCtx *ExecutionContext) string {
	if execCtx != nil && execCtx.Session.SandboxLevel != "" {
		return strings.ToLower(strings.TrimSpace(execCtx.Session.SandboxLevel))
	}
	level := strings.ToLower(strings.TrimSpace(s.cfg.DefaultSandboxLevel))
	if level == "" {
		return "run"
	}
	return level
}

func (s *ContainerHubSandboxService) Execute(ctx context.Context, execCtx *ExecutionContext, command string, cwd string, timeoutMs int64) (SandboxExecutionResult, error) {
	if err := s.OpenIfNeeded(ctx, execCtx); err != nil {
		return SandboxExecutionResult{}, err
	}
	workingDirectory := cwd
	if strings.TrimSpace(workingDirectory) == "" {
		workingDirectory = execCtx.SandboxSession.DefaultCwd
	}
	payload := map[string]any{
		"command": "/bin/sh",
		"args":    []string{"-lc", command},
		"cwd":     workingDirectory,
	}
	if timeoutMs > 0 {
		payload["timeout_ms"] = timeoutMs
	}
	rawText, isJSON, err := s.client.ExecuteSessionRaw(ctx, execCtx.SandboxSession.SessionID, payload)
	if err != nil {
		return SandboxExecutionResult{}, err
	}
	if !isJSON {
		// Success: plain text output (Java: textBody → isTextual → return directly)
		return SandboxExecutionResult{
			ExitCode:         0,
			Stdout:           rawText,
			WorkingDirectory: workingDirectory,
		}, nil
	}
	// Error: JSON response with exitCode/stdout/stderr
	var parsed map[string]any
	_ = json.Unmarshal([]byte(rawText), &parsed)
	exitCode := intValue(parsed["exitCode"], -1)
	return SandboxExecutionResult{
		ExitCode:         exitCode,
		Stdout:           stringValue(parsed["stdout"]),
		Stderr:           stringValue(parsed["stderr"]),
		WorkingDirectory: workingDirectory,
	}, nil
}

func (s *ContainerHubSandboxService) CloseQuietly(execCtx *ExecutionContext) {
	if execCtx == nil || execCtx.SandboxSession == nil {
		return
	}
	session := execCtx.SandboxSession
	switch session.Level {
	case "agent":
		s.releaseAgentSession(execCtx.Session.AgentKey)
	case "global":
	default:
		go func(sessionID string, delay time.Duration) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
			_, _ = s.client.StopSession(context.Background(), sessionID)
		}(session.SessionID, time.Duration(maxInt64(s.cfg.DestroyQueueDelayMs, 0))*time.Millisecond)
	}
	execCtx.SandboxSession = nil
}

func (s *ContainerHubSandboxService) acquireRunSession(ctx context.Context, execCtx *ExecutionContext) error {
	return s.createAndBind(ctx, execCtx, "run", "run-"+execCtx.Session.RunID)
}

func (s *ContainerHubSandboxService) acquireAgentSession(ctx context.Context, execCtx *ExecutionContext) error {
	s.mu.Lock()
	if managed := s.agentSessions[execCtx.Session.AgentKey]; managed != nil {
		managed.activeUsers++
		managed.lastUsed = time.Now()
		execCtx.SandboxSession = &SandboxSession{
			SessionID:     managed.session.SessionID,
			EnvironmentID: managed.session.EnvironmentID,
			DefaultCwd:    filepath.ToSlash(filepath.Join("/workspace", execCtx.Session.ChatID)),
			Level:         "agent",
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.createAndBind(ctx, execCtx, "agent", "agent-"+execCtx.Session.AgentKey); err != nil {
		return err
	}
	s.mu.Lock()
	s.agentSessions[execCtx.Session.AgentKey] = &managedSandboxSession{session: execCtx.SandboxSession, activeUsers: 1, lastUsed: time.Now()}
	s.mu.Unlock()
	return nil
}

func (s *ContainerHubSandboxService) acquireGlobalSession(ctx context.Context, execCtx *ExecutionContext) error {
	s.mu.Lock()
	if s.globalSession != nil {
		s.globalSession.activeUsers++
		s.globalSession.lastUsed = time.Now()
		execCtx.SandboxSession = &SandboxSession{
			SessionID:     s.globalSession.session.SessionID,
			EnvironmentID: s.globalSession.session.EnvironmentID,
			DefaultCwd:    filepath.ToSlash(filepath.Join("/workspace", execCtx.Session.ChatID)),
			Level:         "global",
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.createAndBind(ctx, execCtx, "global", "global-singleton"); err != nil {
		return err
	}
	s.mu.Lock()
	s.globalSession = &managedSandboxSession{session: execCtx.SandboxSession, activeUsers: 1, lastUsed: time.Now()}
	s.mu.Unlock()
	return nil
}

func (s *ContainerHubSandboxService) releaseAgentSession(agentKey string) {
	if agentKey == "" {
		return
	}
	s.mu.Lock()
	managed := s.agentSessions[agentKey]
	if managed == nil {
		s.mu.Unlock()
		return
	}
	managed.activeUsers--
	managed.lastUsed = time.Now()
	sessionID := managed.session.SessionID
	idle := time.Duration(maxInt64(s.cfg.AgentIdleTimeoutMs, 0)) * time.Millisecond
	s.mu.Unlock()
	if idle <= 0 {
		_, _ = s.client.StopSession(context.Background(), sessionID)
		s.mu.Lock()
		delete(s.agentSessions, agentKey)
		s.mu.Unlock()
		return
	}
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		<-timer.C
		s.mu.Lock()
		current := s.agentSessions[agentKey]
		if current == nil || current.activeUsers > 0 || time.Since(current.lastUsed) < idle {
			s.mu.Unlock()
			return
		}
		delete(s.agentSessions, agentKey)
		s.mu.Unlock()
		_, _ = s.client.StopSession(context.Background(), sessionID)
	}()
}

func (s *ContainerHubSandboxService) createAndBind(ctx context.Context, execCtx *ExecutionContext, level string, sessionID string) error {
	mounts, err := s.mounts.Resolve(execCtx.Session.ChatID, execCtx.Session.AgentKey, level, execCtx.Session.SandboxExtraMounts)
	if err != nil {
		return err
	}
	payloadMounts := make([]map[string]any, 0, len(mounts))
	for _, mount := range mounts {
		payloadMounts = append(payloadMounts, map[string]any{
			"source":      mount.Source,
			"destination": mount.Destination,
			"read_only":   mount.ReadOnly,
		})
	}
	response, err := s.client.CreateSession(ctx, map[string]any{
		"session_id":       sessionID,
		"environment_name": execCtx.resolvedEnvironmentID,
		"cwd":              "/workspace",
		"mounts":           payloadMounts,
		"labels": map[string]string{
			"runId":    execCtx.Session.RunID,
			"chatId":   execCtx.Session.ChatID,
			"agentKey": execCtx.Session.AgentKey,
		},
	})
	if err != nil {
		return err
	}
	returnedSessionID := stringValue(response["session_id"])
	if returnedSessionID == "" {
		returnedSessionID = sessionID
	}
	defaultCwd := stringValue(response["cwd"])
	if defaultCwd == "" {
		defaultCwd = filepath.ToSlash(filepath.Join("/workspace", execCtx.Session.ChatID))
	}
	execCtx.SandboxSession = &SandboxSession{
		SessionID:     returnedSessionID,
		EnvironmentID: execCtx.resolvedEnvironmentID,
		DefaultCwd:    defaultCwd,
		Level:         level,
	}
	return nil
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return fallback
	}
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func maxInt64(value int64, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}
