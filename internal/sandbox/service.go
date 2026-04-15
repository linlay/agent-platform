package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

type ContainerHubSandboxService struct {
	cfg           config.ContainerHubConfig
	client        *ContainerHubClient
	mounts        *ContainerHubMountResolver
	mu            sync.Mutex
	agentSessions map[string]*managedSandboxSession
	globalSession *managedSandboxSession
}

type managedSandboxSession struct {
	session     *contracts.SandboxSession
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
func (s *ContainerHubSandboxService) resolveEnvironmentID(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil && execCtx.Session.SandboxEnvironmentID != "" {
		return strings.TrimSpace(execCtx.Session.SandboxEnvironmentID)
	}
	return strings.TrimSpace(s.cfg.DefaultEnvironmentID)
}

func (s *ContainerHubSandboxService) resolveSandboxLevel(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil && execCtx.Session.SandboxLevel != "" {
		return strings.ToLower(strings.TrimSpace(execCtx.Session.SandboxLevel))
	}
	level := strings.ToLower(strings.TrimSpace(s.cfg.DefaultSandboxLevel))
	if level == "" {
		return "run"
	}
	return level
}

func (s *ContainerHubSandboxService) Execute(ctx context.Context, execCtx *contracts.ExecutionContext, command string, cwd string, timeoutMs int64) (contracts.SandboxExecutionResult, error) {
	if err := s.OpenIfNeeded(ctx, execCtx); err != nil {
		return contracts.SandboxExecutionResult{}, err
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
		return contracts.SandboxExecutionResult{}, err
	}
	if !isJSON {
		return contracts.SandboxExecutionResult{
			ExitCode:         0,
			Stdout:           rawText,
			WorkingDirectory: workingDirectory,
		}, nil
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(rawText), &parsed)
	// container-hub error envelope uses snake_case: exit_code / stdout / stderr / working_directory
	exitCode := intValue(parsed["exit_code"], -1)
	if exitCode == -1 {
		// tolerate legacy camelCase variants
		exitCode = intValue(parsed["exitCode"], -1)
	}
	return contracts.SandboxExecutionResult{
		ExitCode:         exitCode,
		Stdout:           stringValue(parsed["stdout"]),
		Stderr:           stringValue(parsed["stderr"]),
		WorkingDirectory: workingDirectory,
	}, nil
}

func (s *ContainerHubSandboxService) CloseQuietly(execCtx *contracts.ExecutionContext) {
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
			if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
				log.Printf("[sandbox] stop session failed id=%s: %v", sessionID, err)
			}
		}(session.SessionID, time.Duration(maxInt64(s.cfg.DestroyQueueDelayMs, 0))*time.Millisecond)
	}
	execCtx.SandboxSession = nil
}

func (s *ContainerHubSandboxService) acquireRunSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	return s.createAndBind(ctx, execCtx, "run", "run-"+execCtx.Session.RunID)
}

func (s *ContainerHubSandboxService) acquireAgentSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	s.mu.Lock()
	if managed := s.agentSessions[execCtx.Session.AgentKey]; managed != nil {
		managed.activeUsers++
		managed.lastUsed = time.Now()
		execCtx.SandboxSession = &contracts.SandboxSession{
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

func (s *ContainerHubSandboxService) acquireGlobalSession(ctx context.Context, execCtx *contracts.ExecutionContext) error {
	s.mu.Lock()
	if s.globalSession != nil {
		s.globalSession.activeUsers++
		s.globalSession.lastUsed = time.Now()
		execCtx.SandboxSession = &contracts.SandboxSession{
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
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop agent session failed id=%s agent=%s: %v", sessionID, agentKey, err)
		}
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
		if _, err := s.client.StopSession(context.Background(), sessionID); err != nil {
			log.Printf("[sandbox] stop idle agent session failed id=%s agent=%s: %v", sessionID, agentKey, err)
		}
	}()
}

func (s *ContainerHubSandboxService) createAndBind(ctx context.Context, execCtx *contracts.ExecutionContext, level string, sessionID string) error {
	mounts, err := s.mounts.Resolve(execCtx.Session.ChatID, execCtx.Session.AgentKey, level, execCtx.Session.SandboxExtraMounts)
	if err != nil {
		return err
	}
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
	response, err := s.client.CreateSession(ctx, map[string]any{
		"session_id":       sessionID,
		"environment_name": environmentID,
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
	execCtx.SandboxSession = &contracts.SandboxSession{
		SessionID:     returnedSessionID,
		EnvironmentID: environmentID,
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
