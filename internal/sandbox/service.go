package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

const workspaceChatSandboxProtocol = "dual-root-v2"

type ContainerHubSandboxService struct {
	cfg            config.ContainerHubConfig
	client         *ContainerHubClient
	mounts         *ContainerHubMountResolver
	mu             sync.Mutex
	runSessions    map[string]*managedSandboxSession
	agentSessions  map[string]*managedSandboxSession
	globalSessions map[string]*managedSandboxSession
}

type managedSandboxSession struct {
	session     *contracts.SandboxSession
	activeUsers int
	lastUsed    time.Time
}

func NewContainerHubSandboxService(cfg config.ContainerHubConfig, paths config.PathsConfig) *ContainerHubSandboxService {
	return &ContainerHubSandboxService{
		cfg:            cfg,
		client:         NewContainerHubClient(cfg),
		mounts:         NewContainerHubMountResolver(paths),
		runSessions:    map[string]*managedSandboxSession{},
		agentSessions:  map[string]*managedSandboxSession{},
		globalSessions: map[string]*managedSandboxSession{},
	}
}

func (s *ContainerHubSandboxService) OpenIfNeeded(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	if execCtx == nil {
		return fmt.Errorf("missing execution context")
	}
	if execCtx.SandboxSession != nil {
		return nil
	}
	if !s.cfg.Enabled {
		return fmt.Errorf("container-hub sandbox is disabled")
	}

	environmentID := s.resolveEnvironmentID(execCtx)
	if environmentID == "" {
		return fmt.Errorf("container-hub environment id is required")
	}
	level := s.resolveRuntimeLevel(execCtx)
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
// agent runtimeConfig.environmentId > global default.
func (s *ContainerHubSandboxService) resolveEnvironmentID(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil && execCtx.Session.RuntimeEnvironmentID != "" {
		return strings.TrimSpace(execCtx.Session.RuntimeEnvironmentID)
	}
	return strings.TrimSpace(s.cfg.DefaultEnvironmentID)
}

func (s *ContainerHubSandboxService) resolveRuntimeLevel(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil && execCtx.Session.RuntimeLevel != "" {
		return strings.ToLower(strings.TrimSpace(execCtx.Session.RuntimeLevel))
	}
	level := strings.ToLower(strings.TrimSpace(s.cfg.DefaultSandboxLevel))
	if level == "" {
		return "run"
	}
	return level
}

func (s *ContainerHubSandboxService) Execute(ctx context.Context, execCtx *contracts.ExecutionContext, command string, cwd string, timeout int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	if err := agentconfig.ValidateUserEnvironment(env); err != nil {
		return contracts.SandboxExecutionResult{}, err
	}
	if err := s.OpenIfNeeded(ctx, execCtx); err != nil {
		return contracts.SandboxExecutionResult{}, err
	}
	executionCwd := strings.TrimSpace(cwd)
	if executionCwd == "" {
		executionCwd = strings.TrimSpace(execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir)
	}
	if executionCwd == "" {
		return contracts.SandboxExecutionResult{}, fmt.Errorf("workspace_unavailable: sandbox execution requires a workspace")
	}
	payload := map[string]any{
		"command": "/bin/sh",
		"args":    []string{"-lc", command},
		"cwd":     executionCwd,
	}
	if timeout > 0 {
		payload["timeout"] = timeout
	}
	executionEnv, err := sandboxCommandEnvironment(execCtx, env)
	if err != nil {
		return contracts.SandboxExecutionResult{}, fmt.Errorf("snapshot run environment: %w", err)
	}
	if len(executionEnv) > 0 {
		payload["env"] = executionEnv
	}
	rawText, isJSON, err := s.client.ExecuteSessionRaw(ctx, execCtx.SandboxSession.SessionID, payload)
	if err != nil {
		return contracts.SandboxExecutionResult{}, err
	}
	if !isJSON {
		return contracts.SandboxExecutionResult{
			ExitCode: 0,
			Stdout:   rawText,
			Cwd:      executionCwd,
		}, nil
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(rawText), &parsed)
	// Container Hub uses snake_case for command results. Its reported cwd is
	// intentionally ignored; Platform's requested cwd remains authoritative.
	exitCode := intValue(parsed["exit_code"], -1)
	return contracts.SandboxExecutionResult{
		ExitCode: exitCode,
		Stdout:   stringValue(parsed["stdout"]),
		Stderr:   stringValue(parsed["stderr"]),
		Cwd:      executionCwd,
	}, nil
}

func (s *ContainerHubSandboxService) CloseQuietly(execCtx *contracts.ExecutionContext) {
	if execCtx == nil || execCtx.SandboxSession == nil {
		return
	}
	session := execCtx.SandboxSession
	switch session.Level {
	case "agent":
		s.releaseAgentSession(session.ReuseKey)
	case "global":
	default:
		s.releaseRunSession(session.ReuseKey)
	}
	execCtx.SandboxSession = nil
}

func (s *ContainerHubSandboxService) acquireRunSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	mounts, maskedPaths, fingerprint, err := s.resolveSessionMountIdentity(execCtx, "run")
	if err != nil {
		return err
	}
	sessionKey := runSessionID(execCtx.Session, fingerprint)
	s.mu.Lock()
	if managed := s.runSessions[sessionKey]; managed != nil {
		managed.activeUsers++
		managed.lastUsed = time.Now()
		execCtx.SandboxSession = &contracts.SandboxSession{
			SessionID:     managed.session.SessionID,
			EnvironmentID: managed.session.EnvironmentID,
			Level:         "run",
			ReuseKey:      sessionKey,
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.createAndBind(ctx, execCtx, "run", sessionKey, sessionKey, mounts, maskedPaths); err != nil {
		return err
	}
	s.mu.Lock()
	s.runSessions[sessionKey] = &managedSandboxSession{session: execCtx.SandboxSession, activeUsers: 1, lastUsed: time.Now()}
	s.mu.Unlock()
	return nil
}

func runSessionID(session contracts.QuerySession, fingerprint string) string {
	runID := strings.TrimSpace(session.RunID)
	subTaskID := strings.TrimSpace(session.SubTaskID)
	if subTaskID == "" {
		return "run-" + runID + "-" + fingerprint
	}
	return "run-" + runID + "-" + subTaskID + "-" + fingerprint
}

func (s *ContainerHubSandboxService) acquireAgentSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	mounts, maskedPaths, fingerprint, err := s.resolveSessionMountIdentity(execCtx, "agent")
	if err != nil {
		return err
	}
	sessionKey := agentChatSessionKey(execCtx.Session, fingerprint)
	s.mu.Lock()
	if managed := s.agentSessions[sessionKey]; managed != nil {
		managed.activeUsers++
		managed.lastUsed = time.Now()
		execCtx.SandboxSession = &contracts.SandboxSession{
			SessionID:     managed.session.SessionID,
			EnvironmentID: managed.session.EnvironmentID,
			Level:         "agent",
			ReuseKey:      sessionKey,
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.createAndBind(ctx, execCtx, "agent", scopedSandboxSessionID("agent", sessionKey), sessionKey, mounts, maskedPaths); err != nil {
		return err
	}
	s.mu.Lock()
	s.agentSessions[sessionKey] = &managedSandboxSession{session: execCtx.SandboxSession, activeUsers: 1, lastUsed: time.Now()}
	s.mu.Unlock()
	return nil
}

func (s *ContainerHubSandboxService) acquireGlobalSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	mounts, maskedPaths, fingerprint, err := s.resolveSessionMountIdentity(execCtx, "global")
	if err != nil {
		return err
	}
	sessionKey := agentChatSessionKey(execCtx.Session, fingerprint)
	s.mu.Lock()
	if managed := s.globalSessions[sessionKey]; managed != nil {
		managed.activeUsers++
		managed.lastUsed = time.Now()
		execCtx.SandboxSession = &contracts.SandboxSession{
			SessionID:     managed.session.SessionID,
			EnvironmentID: managed.session.EnvironmentID,
			Level:         "global",
			ReuseKey:      sessionKey,
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.createAndBind(ctx, execCtx, "global", scopedSandboxSessionID("global", sessionKey), sessionKey, mounts, maskedPaths); err != nil {
		return err
	}
	s.mu.Lock()
	s.globalSessions[sessionKey] = &managedSandboxSession{session: execCtx.SandboxSession, activeUsers: 1, lastUsed: time.Now()}
	s.mu.Unlock()
	return nil
}

func agentChatSessionKey(session contracts.QuerySession, fingerprint string) string {
	return strings.TrimSpace(session.AgentKey) + "\x00" +
		strings.TrimSpace(session.ChatID) + "\x00" +
		fingerprint
}

func (s *ContainerHubSandboxService) resolveSessionMountIdentity(execCtx *contracts.ExecutionContext, level string) ([]MountSpec, []string, string, error) {
	layout, err := s.mounts.ResolveLayout(
		execCtx.Session.WorkspaceRoot,
		execCtx.Session.ChatID,
		execCtx.Session.AgentKey,
		level,
		execCtx.Session.RuntimeExtraMounts,
	)
	if err != nil {
		return nil, nil, "", err
	}
	mounts := layout.Mounts
	maskedPaths := layout.MaskedPaths
	if strings.EqualFold(strings.TrimSpace(s.cfg.ResolvedEngine), "local") {
		mounts = localEngineMounts(mounts)
		maskedPaths = nil
	}
	raw, _ := json.Marshal(struct {
		Protocol        string
		EnvironmentID   string
		WorkspaceSource string
		ChatSource      string
		Environment     map[string]string
		Mounts          []MountSpec
		MaskedPaths     []string
	}{
		Protocol:        workspaceChatSandboxProtocol,
		EnvironmentID:   s.resolveEnvironmentID(execCtx),
		WorkspaceSource: mountSource(mounts, "/workspace"),
		ChatSource:      mountSource(mounts, "/chat"),
		Environment:     sandboxSessionEnvironment(execCtx),
		Mounts:          mounts,
		MaskedPaths:     maskedPaths,
	})
	sum := sha256.Sum256(raw)
	return mounts, maskedPaths, fmt.Sprintf("%x", sum[:8]), nil
}

func localEngineMounts(mounts []MountSpec) []MountSpec {
	out := append([]MountSpec(nil), mounts...)
	for index := range out {
		switch out[index].Destination {
		case "/workspace", "/chat":
			out[index].Destination = out[index].Source
		}
	}
	return out
}

func mountSource(mounts []MountSpec, destination string) string {
	for _, mount := range mounts {
		if mount.Destination == destination ||
			(destination == "/workspace" && mount.Name == "workspace") ||
			(destination == "/chat" && mount.Name == "chat-dir") {
			return mount.Source
		}
	}
	return ""
}

func scopedSandboxSessionID(prefix string, sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return strings.TrimSpace(prefix) + "-" + fmt.Sprintf("%x", sum[:12])
}

func (s *ContainerHubSandboxService) releaseAgentSession(sessionKey string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	managed := s.agentSessions[sessionKey]
	if managed == nil {
		s.mu.Unlock()
		return
	}
	managed.activeUsers--
	managed.lastUsed = time.Now()
	sessionID := managed.session.SessionID
	idle := time.Duration(maxInt64(s.cfg.AgentIdleTimeout, 0)) * time.Second
	s.mu.Unlock()
	if idle <= 0 {
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop agent session failed id=%s scope=%q: %v", sessionID, sessionKey, err)
		}
		s.mu.Lock()
		delete(s.agentSessions, sessionKey)
		s.mu.Unlock()
		return
	}
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		<-timer.C
		s.mu.Lock()
		current := s.agentSessions[sessionKey]
		if current == nil || current.activeUsers > 0 || time.Since(current.lastUsed) < idle {
			s.mu.Unlock()
			return
		}
		delete(s.agentSessions, sessionKey)
		s.mu.Unlock()
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop idle agent session failed id=%s scope=%q: %v", sessionID, sessionKey, err)
		}
	}()
}

func (s *ContainerHubSandboxService) releaseRunSession(sessionKey string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	managed := s.runSessions[sessionKey]
	if managed == nil {
		s.mu.Unlock()
		return
	}
	managed.activeUsers--
	managed.lastUsed = time.Now()
	sessionID := managed.session.SessionID
	idle := time.Duration(maxInt64(s.cfg.DestroyQueueDelay, 0)) * time.Second
	s.mu.Unlock()
	if idle <= 0 {
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop run session failed id=%s key=%s: %v", sessionID, sessionKey, err)
		}
		s.mu.Lock()
		delete(s.runSessions, sessionKey)
		s.mu.Unlock()
		return
	}
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		<-timer.C
		s.mu.Lock()
		current := s.runSessions[sessionKey]
		if current == nil || current.activeUsers > 0 || time.Since(current.lastUsed) < idle {
			s.mu.Unlock()
			return
		}
		delete(s.runSessions, sessionKey)
		s.mu.Unlock()
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop idle run session failed id=%s key=%s: %v", sessionID, sessionKey, err)
		}
	}()
}

func (s *ContainerHubSandboxService) createAndBind(
	ctx context.Context,
	execCtx *contracts.ExecutionContext,
	level string,
	sessionID string,
	reuseKey string,
	mounts []MountSpec,
	maskedPaths []string,
) error {
	environmentID := s.resolveEnvironmentID(execCtx)
	if environmentID == "" {
		return fmt.Errorf("container-hub environment id is required")
	}
	payloadMounts := make([]map[string]any, 0, len(mounts))
	for _, mount := range mounts {
		payloadMounts = append(payloadMounts, map[string]any{
			"source":      mount.Source,
			"destination": mount.Destination,
			"read_only":   mount.ReadOnly,
		})
	}
	payload := map[string]any{
		"session_id":        sessionID,
		"environment_name":  environmentID,
		"cwd":               sandboxWorkspaceCwd(execCtx),
		"mounts":            payloadMounts,
		"workspaceProtocol": workspaceChatSandboxProtocol,
		"labels": map[string]string{
			"runId":             execCtx.Session.RunID,
			"chatId":            execCtx.Session.ChatID,
			"agentKey":          execCtx.Session.AgentKey,
			"workspaceProtocol": workspaceChatSandboxProtocol,
		},
	}
	if len(maskedPaths) > 0 {
		if err := s.client.RequireWorkspaceProtocol(ctx, workspaceChatSandboxProtocol); err != nil {
			return err
		}
		payload["masked_paths"] = append([]string(nil), maskedPaths...)
	}
	if sessionEnv := sandboxSessionEnvironment(execCtx); len(sessionEnv) > 0 {
		payload["env"] = sessionEnv
	}
	response, err := s.client.CreateSession(ctx, payload)
	if err != nil {
		return err
	}
	returnedSessionID := stringValue(response["session_id"])
	if returnedSessionID == "" {
		returnedSessionID = sessionID
	}
	execCtx.SandboxSession = &contracts.SandboxSession{
		SessionID:     returnedSessionID,
		EnvironmentID: environmentID,
		Level:         level,
		ReuseKey:      reuseKey,
	}
	return nil
}

func sandboxWorkspaceCwd(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil {
		if workspace := strings.TrimSpace(execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir); workspace != "" {
			return workspace
		}
	}
	return "/workspace"
}

func sandboxSessionEnvironment(execCtx *contracts.ExecutionContext) map[string]string {
	if execCtx == nil {
		return nil
	}
	return agentconfig.Merge(
		execCtx.StaticRuntimeEnv,
		agentconfig.ContainerEnvironment(
			execCtx.Session.RuntimeContext.SandboxPaths.AgentDir,
			execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir,
			execCtx.Session.RuntimeContext.SandboxPaths.ChatDir,
		),
	)
}

func sandboxCommandEnvironment(execCtx *contracts.ExecutionContext, invocationEnv map[string]string) (map[string]string, error) {
	if execCtx == nil {
		return contracts.CloneStringMap(invocationEnv), nil
	}
	dynamic := map[string]string(nil)
	if execCtx.RunEnvironment != nil {
		var err error
		dynamic, _, err = execCtx.RunEnvironment.Snapshot()
		if err != nil {
			return nil, err
		}
	}
	return agentconfig.Merge(
		execCtx.StaticRuntimeEnv,
		dynamic,
		invocationEnv,
		agentconfig.ContainerEnvironment(
			execCtx.Session.RuntimeContext.SandboxPaths.AgentDir,
			execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir,
			execCtx.Session.RuntimeContext.SandboxPaths.ChatDir,
		),
	), nil
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
