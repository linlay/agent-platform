package engine

import (
	"context"
	"time"

	"agent-platform-runner-go/internal/api"
)

type AgentEngine interface {
	Stream(ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

type AgentStream interface {
	Next() (AgentDelta, error)
	Close() error
}

type ActiveRunService interface {
	Register(parent context.Context, session QuerySession) (context.Context, *RunControl, ActiveRun)
	Submit(req api.SubmitRequest) SubmitAck
	Steer(req api.SteerRequest) SteerAck
	Interrupt(req api.InterruptRequest) InterruptAck
	Finish(runID string)
}

type RunManager = ActiveRunService

type ToolExecutor interface {
	Definitions() []api.ToolDetailResponse
	Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
}

type ActionInvoker interface {
	Invoke(ctx context.Context, actionName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
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
	RequestID             string
	RunID                 string
	ChatID                string
	ChatName              string
	AgentKey              string
	AgentName             string
	ModelKey              string
	ToolNames             []string
	Mode                  string
	TeamID                string
	Created               bool
	Subject               string
	SkillKeys             []string
	ContextTags           []string
	Budget                map[string]any
	StageSettings         map[string]any
	ToolOverrides         map[string]api.ToolDetailResponse
	ResolvedBudget        Budget
	ResolvedStageSettings PlanExecuteSettings
	HistoryMessages       []map[string]any
	MemoryContext         string

	// Prompt files loaded from agent directory.
	SoulPrompt    string
	AgentsPrompt  string
	PlanPrompt    string
	ExecutePrompt string
	SummaryPrompt string
}

type ExecutionContext struct {
	Request         api.QueryRequest
	Session         QuerySession
	RunControl      *RunControl
	CurrentToolID   string
	CurrentToolName string
	SandboxSession  *SandboxSession
	Budget          Budget
	StageSettings   PlanExecuteSettings
	RunLoopState    RunLoopState
	PlanState       *PlanRuntimeState
	ToolOverrides   map[string]api.ToolDetailResponse
	StartedAt       time.Time
	ModelCalls      int
	ToolCalls       int
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

type RunLoopState string

const (
	RunLoopStateIdle           RunLoopState = "IDLE"
	RunLoopStateModelStreaming RunLoopState = "MODEL_STREAMING"
	RunLoopStateToolExecuting  RunLoopState = "TOOL_EXECUTING"
	RunLoopStateWaitingSubmit  RunLoopState = "WAITING_SUBMIT"
	RunLoopStateCompleted      RunLoopState = "COMPLETED"
	RunLoopStateCancelled      RunLoopState = "CANCELLED"
	RunLoopStateFailed         RunLoopState = "FAILED"
)

type PlanTask struct {
	TaskID      string
	Description string
	Status      string
}

type PlanRuntimeState struct {
	PlanID       string
	Tasks        []PlanTask
	ActiveTaskID string
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

type NoopActionInvoker struct{}

func NewNoopActionInvoker() *NoopActionInvoker { return &NoopActionInvoker{} }

func (n *NoopActionInvoker) Invoke(_ context.Context, actionName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	return ToolExecutionResult{
		Output:     "status: not_implemented",
		Structured: map[string]any{"actionName": actionName, "args": args, "status": "not_implemented"},
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
