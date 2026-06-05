package contracts

import (
	"context"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

const (
	AccessLevelDefault     = "default"
	AccessLevelAutoApprove = "auto_approve"
	AccessLevelFullAccess  = "full_access"
)

func NormalizeAccessLevel(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return AccessLevelDefault, true
	}
	switch normalized {
	case AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess:
		return normalized, true
	default:
		return "", false
	}
}

type AgentEngine interface {
	Stream(ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

type AgentStream interface {
	Next() (AgentDelta, error)
	Close() error
}

type OrchestratableAgentStream interface {
	AgentStream
	InjectToolResult(toolID string, text string, isError bool) bool
	FinalAssistantContent() (string, bool)
}

type StreamDeltaMapper interface {
	Map(delta AgentDelta) []stream.StreamInput
	CloneIsolated(runID string, chatID string) StreamDeltaMapper
}

type StreamDeltaMapperFactory interface {
	NewDeltaMapper(runID string, chatID string, budget Budget, toolRegistry ToolDefinitionLookup) StreamDeltaMapper
}

type SystemInitProfile struct {
	CacheKey      string
	Mode          string
	Stage         string
	Fingerprint   string
	SystemMessage map[string]any
	Tools         []any
}

type SystemInitBuilder interface {
	BuildSystemInitProfiles(session QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, defaultPlanMaxSteps int, defaultPlanMaxWorkRoundsPerTask int, prompts config.PromptsConfig) []SystemInitProfile
}

type ActiveRunService interface {
	Register(parent context.Context, session QuerySession) (context.Context, *RunControl, ActiveRun)
	LookupAwaiting(runID string, awaitingID string) (AwaitingSubmitContext, bool)
	Submit(req api.SubmitRequest) SubmitAck
	Steer(req api.SteerRequest) SteerAck
	Interrupt(req api.InterruptRequest) InterruptAck
	UpdateAccessLevel(req api.AccessLevelRequest) AccessLevelAck
	Finish(runID string)
	AttachObserver(runID string, afterSeq int64) (*stream.Observer, error)
	DetachObserver(runID string, observerID string)
	EventBus(runID string) (*stream.RunEventBus, bool)
	RunStatus(runID string) (RunStatusInfo, bool)
	ActiveRunForChat(chatID string) (RunStatusInfo, bool, error)
}

type RunManager = ActiveRunService

type ToolExecutor interface {
	Definitions() []api.ToolDetailResponse
	Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
}

type FileChangeHook interface {
	AfterFileChange(ctx context.Context, event FileChangeEvent) FileChangeHookResult
}

type FileChangeEvent struct {
	WorkspaceRoot string
	FilePath      string
	Operation     string
	LanguageID    string
	ContentSHA256 string
	Content       []byte
	LineStats     LineDiffStats
}

type LineDiffStats struct {
	AddedLines   int `json:"addedLines"`
	DeletedLines int `json:"deletedLines"`
	EditedLines  int `json:"editedLines"`
}

type FileChangeHookResult struct {
	Name        string          `json:"name,omitempty"`
	Status      string          `json:"status,omitempty"`
	LanguageID  string          `json:"languageId,omitempty"`
	FilePath    string          `json:"filePath,omitempty"`
	Diagnostics []LSPDiagnostic `json:"diagnostics,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Message     string          `json:"message,omitempty"`
}

type LSPDiagnostic struct {
	Severity string   `json:"severity,omitempty"`
	Message  string   `json:"message,omitempty"`
	Source   string   `json:"source,omitempty"`
	Code     string   `json:"code,omitempty"`
	Range    LSPRange `json:"range"`
}

type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type ActionInvoker interface {
	Invoke(ctx context.Context, actionName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
}

type SandboxClient interface {
	OpenIfNeeded(ctx context.Context, execCtx *ExecutionContext) error
	Execute(ctx context.Context, execCtx *ExecutionContext, command string, cwd string, timeoutMs int64, env map[string]string) (SandboxExecutionResult, error)
	CloseQuietly(execCtx *ExecutionContext)
}

type McpClient interface {
	CallTool(ctx context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error)
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
	// SubTaskID, when non-empty, isolates sandbox session for a sub-agent
	// child task within the same run. Empty for main-agent sessions so they
	// share the run-level sandbox.
	SubTaskID             string
	ChatID                string
	ChatName              string
	AgentKey              string
	AgentName             string
	AgentRole             string
	AgentDescription      string
	AgentType             string
	ModelKey              string
	ToolNames             []string
	Mode                  string
	PlanningMode          bool
	ReactMaxSteps         int
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
	StableMemoryContext   string
	SessionMemoryContext  string
	ObservationContext    string
	WorkflowContext       string
	MemoryUsageSummary    *api.MemoryUsageSummary
	RuntimeContext        RuntimeRequestContext
	PromptAppend          PromptAppendConfig
	StaticMemoryPrompt    string
	SkillCatalogPrompt    string
	SystemInitCache       map[string]SystemInitSnapshot

	// Prompt files loaded from agent directory.
	SoulPrompt            string
	AgentsPrompt          string
	WorkspaceAgentsPrompt string
	PlanPrompt            string
	ExecutePrompt         string
	SummaryPrompt         string
	CoderSystemPrompt     string

	RuntimeEnvironmentID   string
	RuntimeLevel           string
	RuntimeExtraMounts     []SandboxExtraMount
	RuntimeHostAccess      HostAccessRoots
	AgentHasRuntimeSandbox bool
	AgentHasMemoryConfig   bool
	WorkspaceRoot          string
	AccessLevel            string
	SkillHookDirs          []string
	// RuntimeEnvOverrides carries agent/skill-level env defaults for both sandbox and host bash execution.
	RuntimeEnvOverrides map[string]string
}

type SystemInitSnapshot struct {
	Fingerprint   string
	SystemMessage map[string]any
	Tools         []any
}

type SandboxExtraMount struct {
	Platform    string
	Source      string
	Destination string
	Mode        string
}

type HostAccessRoots struct {
	ReadRoots  []string
	WriteRoots []string
}

type ReadFileSnapshot struct {
	ModifiedUnixMs int64
	SizeBytes      int64
	SHA256         string
	Offset         int64
	Limit          int64
	ReadAtUnixMs   int64
	Source         string
	LineNumbered   bool
	Partial        bool
	Truncated      bool
}

type ExecutionContext struct {
	Request           api.QueryRequest
	Session           QuerySession
	RunControl        *RunControl
	CurrentToolID     string
	CurrentToolName   string
	HITLLevel         int
	AutoApproveLevels map[int]bool
	SandboxSession    *SandboxSession
	Budget            Budget
	StageSettings     PlanExecuteSettings
	RunLoopState      RunLoopState
	PlanState         *PlanRuntimeState
	PlanningState     *PlanningRuntimeState
	PlanningRevision  int
	ToolOverrides     map[string]api.ToolDetailResponse
	// RuntimeEnvOverrides is reused by host bash as agent/skill-level env defaults.
	RuntimeEnvOverrides map[string]string
	AccessLevel         string
	// AccessPolicyApprovals stores one-shot approvals for exact host bash access-policy fingerprints.
	AccessPolicyApprovals map[string]int
	// AccessPolicyRuleApprovals stores run-scoped approvals for host bash access-policy rules.
	AccessPolicyRuleApprovals map[string]bool
	// BashSecurityApprovals stores one-shot approvals for exact host bash command fingerprints.
	BashSecurityApprovals map[string]int
	// FileReadApprovals stores one-shot approvals for exact read/glob/grep file access paths.
	FileReadApprovals map[string]int
	// FileReadRuleApprovals stores run-scoped approvals for read/glob/grep access under a directory root.
	FileReadRuleApprovals map[string]bool
	// FileAccessApprovals stores one-shot approvals for exact non-read file access paths.
	FileAccessApprovals map[string]int
	// FileAccessRuleApprovals stores run-scoped approvals for non-read file access under a directory root.
	FileAccessRuleApprovals map[string]bool
	// FileWriteApprovals stores one-shot approvals for exact structured write plans.
	FileWriteApprovals map[string]int
	// FileWriteRuleApprovals stores run-scoped approvals for write operation classes under allowed roots.
	FileWriteRuleApprovals map[string]bool
	// ReadFileState tracks file versions read during the current run for write staleness checks.
	ReadFileState map[string]ReadFileSnapshot
	StartedAt     time.Time
	ModelCalls    int
	ToolCalls     int
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
	RawParams  any
	HITL       map[string]any
	Error      string
	ExitCode   int
	SubmitInfo *SubmitInfo
}

type SubmitInfo struct {
	RunID      string
	AwaitingID string
	SubmitID   string
	Params     any
}

type AwaitingSubmitContext struct {
	AwaitingID string
	Mode       string
	ItemCount  int
	NoTimeout  bool
	TimeoutMs  int64
}

func (c AwaitingSubmitContext) Clone() AwaitingSubmitContext {
	return AwaitingSubmitContext{
		AwaitingID: c.AwaitingID,
		Mode:       c.Mode,
		ItemCount:  c.ItemCount,
		NoTimeout:  c.NoTimeout,
		TimeoutMs:  c.TimeoutMs,
	}
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

type RunStatusInfo struct {
	RunID              string
	ChatID             string
	AgentKey           string
	State              RunLoopState
	LastSeq            int64
	OldestSeq          int64
	ObserverCount      int
	StartedAt          int64
	CompletedAt        int64
	AccessLevel        string
	AccessLevelVersion int64
}

type RunLifecycleConfigurer interface {
	ConfigureRunLifecycle(cfg config.RunConfig)
}

type SubmitAck struct {
	Accepted bool
	Status   string
	SubmitID string
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

type AccessLevelAck struct {
	Accepted            bool
	Status              string
	PreviousAccessLevel string
	AccessLevel         string
	Version             int64
	Detail              string
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

type PlanningRuntimeState struct {
	PlanningID   string
	PlanningFile string
	Markdown     string
}

type NoopToolExecutor struct{}

func NewNoopToolExecutor() *NoopToolExecutor { return &NoopToolExecutor{} }

func (n *NoopToolExecutor) Invoke(_ context.Context, toolName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	result := ToolExecutionResult{
		Output:     "status: not_implemented",
		Structured: map[string]any{"toolName": toolName, "args": args, "status": "not_implemented"},
		Error:      "not_implemented",
		ExitCode:   -1,
	}
	return result, ErrNotImplemented
}

type NoopActionInvoker struct{}

func NewNoopActionInvoker() *NoopActionInvoker { return &NoopActionInvoker{} }

func (n *NoopActionInvoker) Invoke(_ context.Context, actionName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	result := ToolExecutionResult{
		Output:     "status: not_implemented",
		Structured: map[string]any{"actionName": actionName, "args": args, "status": "not_implemented"},
		Error:      "not_implemented",
		ExitCode:   -1,
	}
	return result, ErrNotImplemented
}

type NoopSandboxClient struct{}

func NewNoopSandboxClient() *NoopSandboxClient { return &NoopSandboxClient{} }

func (n *NoopSandboxClient) OpenIfNeeded(_ context.Context, _ *ExecutionContext) error { return nil }

func (n *NoopSandboxClient) Execute(_ context.Context, _ *ExecutionContext, command string, cwd string, _ int64, _ map[string]string) (SandboxExecutionResult, error) {
	result := SandboxExecutionResult{
		ExitCode:         -1,
		Stdout:           "",
		Stderr:           "status: not_implemented",
		WorkingDirectory: cwd,
	}
	return result, ErrNotImplemented
}

func (n *NoopSandboxClient) CloseQuietly(_ *ExecutionContext) {}

type NoopMcpClient struct{}

func NewNoopMcpClient() *NoopMcpClient { return &NoopMcpClient{} }

func (n *NoopMcpClient) CallTool(_ context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error) {
	return map[string]any{"serverKey": serverKey, "toolName": toolName, "args": args, "meta": meta, "status": "not_implemented"}, nil
}

type NoopViewportClient struct{}

func NewNoopViewportClient() *NoopViewportClient { return &NoopViewportClient{} }

func (n *NoopViewportClient) Get(_ context.Context, viewportKey string) (map[string]any, error) {
	return map[string]any{"viewportKey": viewportKey, "status": "not_implemented"}, nil
}

func normalizeSteerID(steerID string) string {
	if steerID != "" {
		return steerID
	}
	return time.Now().UTC().Format("20060102150405.000000000")
}
