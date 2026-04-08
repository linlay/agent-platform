package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
//   → success: textBody(response.body()) returns raw text as-is
//   → failure: parsed as JSON error
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

func (r *ContainerHubMountResolver) Resolve(chatID string, agentKey string, level string) ([]MountSpec, error) {
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

	skillsSource, err := r.skillsSource(agentKey, level)
	if err != nil {
		return nil, err
	}
	if skillsSource != "" {
		mounts = append(mounts, MountSpec{Name: "skills-dir", Source: skillsSource, Destination: "/skills", ReadOnly: true})
	}

	if agentDir, err := r.agentSource(agentKey); err == nil && agentDir != "" {
		mounts = append(mounts, MountSpec{Name: "agent-self", Source: agentDir, Destination: "/agent", ReadOnly: true})
	} else if err != nil {
		return nil, err
	}

	return mounts, nil
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
	if agentKey == "" {
		return "", nil
	}
	agentsRoot, err := hostPath("AGENTS_DIR", r.paths.AgentsDir)
	if err != nil {
		return "", fmt.Errorf("container-hub mount validation failed for agent-self: %w", err)
	}
	agentDir := filepath.Join(agentsRoot, agentKey)
	if stat, err := os.Stat(agentDir); err == nil && stat.IsDir() {
		return agentDir, nil
	}
	return "", nil
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
	mounts, err := s.mounts.Resolve(execCtx.Session.ChatID, execCtx.Session.AgentKey, level)
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
