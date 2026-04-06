package engine

import (
	"context"
	"sync"
	"time"

	"agent-platform-runner-go/internal/api"
)

type AgentEngine interface {
	Stream(ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

type AgentStream interface {
	Next() (map[string]any, error)
	Close() error
}

type RunManager interface {
	Register(runID string, chatID string, agentKey string) ActiveRun
	Submit(req api.SubmitRequest) SubmitAck
	Steer(req api.SteerRequest) SteerAck
	Interrupt(req api.InterruptRequest) InterruptAck
	Finish(runID string)
}

type ToolExecutor interface {
	Definitions() []api.ToolDetailResponse
	Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
}

type SandboxClient interface {
	OpenIfNeeded(ctx context.Context, execCtx *ExecutionContext) error
	Execute(ctx context.Context, execCtx *ExecutionContext, command string, cwd string, timeoutMs int64) (SandboxExecutionResult, error)
	CloseQuietly(execCtx *ExecutionContext)
}

type McpClient interface {
	CallTool(ctx context.Context, serverKey string, toolName string, args map[string]any) (map[string]any, error)
}

type ViewportClient interface {
	Get(ctx context.Context, viewportKey string) (map[string]any, error)
}

type CatalogReloader interface {
	Reload(ctx context.Context, reason string) error
}

type QuerySession struct {
	RequestID string
	RunID     string
	ChatID    string
	ChatName  string
	AgentKey  string
	AgentName string
	ModelKey  string
	ToolNames []string
	Mode      string
	TeamID    string
	Created   bool
	Subject   string
}

type ExecutionContext struct {
	Request        api.QueryRequest
	Session        QuerySession
	SandboxSession *SandboxSession
}

type SandboxSession struct {
	SessionID     string
	EnvironmentID string
	DefaultCwd    string
	Level         string
}

type ToolExecutionResult struct {
	Output     string
	Structured map[string]any
	Error      string
	ExitCode   int
}

type SandboxExecutionResult struct {
	ExitCode         int
	Stdout           string
	Stderr           string
	WorkingDirectory string
}

type ActiveRun struct {
	RunID    string
	ChatID   string
	AgentKey string
}

type SubmitAck struct {
	Accepted bool
	Status   string
	Detail   string
}

type SteerAck struct {
	Accepted bool
	Status   string
	SteerID  string
	Detail   string
}

type InterruptAck struct {
	Accepted bool
	Status   string
	Detail   string
}

type InMemoryRunManager struct {
	mu   sync.Mutex
	runs map[string]ActiveRun
}

func NewInMemoryRunManager() *InMemoryRunManager {
	return &InMemoryRunManager{
		runs: map[string]ActiveRun{},
	}
}

func (m *InMemoryRunManager) Register(runID string, chatID string, agentKey string) ActiveRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := ActiveRun{RunID: runID, ChatID: chatID, AgentKey: agentKey}
	m.runs[runID] = run
	return run
}

func (m *InMemoryRunManager) Submit(req api.SubmitRequest) SubmitAck {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[req.RunID]; !ok {
		return SubmitAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	return SubmitAck{Accepted: true, Status: "accepted", Detail: "Frontend submit accepted"}
}

func (m *InMemoryRunManager) Steer(req api.SteerRequest) SteerAck {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[req.RunID]; !ok {
		return SteerAck{Accepted: false, Status: "unmatched", SteerID: normalizeSteerID(req.SteerID), Detail: "No active run found"}
	}
	return SteerAck{Accepted: true, Status: "accepted", SteerID: normalizeSteerID(req.SteerID), Detail: "Steer accepted"}
}

func (m *InMemoryRunManager) Interrupt(req api.InterruptRequest) InterruptAck {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[req.RunID]; !ok {
		return InterruptAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	delete(m.runs, req.RunID)
	return InterruptAck{Accepted: true, Status: "accepted", Detail: "Interrupt accepted"}
}

func (m *InMemoryRunManager) Finish(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.runs, runID)
}

type NoopToolExecutor struct{}

func NewNoopToolExecutor() *NoopToolExecutor { return &NoopToolExecutor{} }

func (n *NoopToolExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (n *NoopToolExecutor) Invoke(_ context.Context, toolName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	return ToolExecutionResult{
		Output:     "status: not_implemented",
		Structured: map[string]any{"toolName": toolName, "args": args, "status": "not_implemented"},
		Error:      "not_implemented",
		ExitCode:   -1,
	}, nil
}

type NoopSandboxClient struct{}

func NewNoopSandboxClient() *NoopSandboxClient { return &NoopSandboxClient{} }

func (n *NoopSandboxClient) OpenIfNeeded(_ context.Context, _ *ExecutionContext) error { return nil }

func (n *NoopSandboxClient) Execute(_ context.Context, _ *ExecutionContext, command string, cwd string, _ int64) (SandboxExecutionResult, error) {
	return SandboxExecutionResult{
		ExitCode:         -1,
		Stdout:           "",
		Stderr:           "status: not_implemented",
		WorkingDirectory: cwd,
	}, nil
}

func (n *NoopSandboxClient) CloseQuietly(_ *ExecutionContext) {}

type NoopMcpClient struct{}

func NewNoopMcpClient() *NoopMcpClient { return &NoopMcpClient{} }

func (n *NoopMcpClient) CallTool(_ context.Context, serverKey string, toolName string, args map[string]any) (map[string]any, error) {
	return map[string]any{"serverKey": serverKey, "toolName": toolName, "args": args, "status": "not_implemented"}, nil
}

type NoopViewportClient struct{}

func NewNoopViewportClient() *NoopViewportClient { return &NoopViewportClient{} }

func (n *NoopViewportClient) Get(_ context.Context, viewportKey string) (map[string]any, error) {
	return map[string]any{"viewportKey": viewportKey, "status": "not_implemented"}, nil
}

type NoopCatalogReloader struct{}

func NewNoopCatalogReloader() *NoopCatalogReloader { return &NoopCatalogReloader{} }

func (n *NoopCatalogReloader) Reload(_ context.Context, reason string) error {
	_ = reason
	return nil
}

func normalizeSteerID(steerID string) string {
	if steerID != "" {
		return steerID
	}
	return time.Now().UTC().Format("20060102150405.000000000")
}
