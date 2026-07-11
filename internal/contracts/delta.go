package contracts

import (
	"agent-platform/internal/api"
	"agent-platform/internal/stream"
)

type AgentDelta interface {
	agentDeltaTag()
}

type DeltaContent struct {
	Text      string
	ContentID string
}

func (DeltaContent) agentDeltaTag() {}

type DeltaReasoning struct {
	Text           string
	ReasoningID    string
	ReasoningLabel string
}

func (DeltaReasoning) agentDeltaTag() {}

type DeltaToolCall struct {
	Index     int
	ID        string
	Name      string
	ArgsDelta string
}

func (DeltaToolCall) agentDeltaTag() {}

type DeltaToolEnd struct {
	ToolIDs     []string
	FileChanges map[string]map[string]any
}

func (DeltaToolEnd) agentDeltaTag() {}

type DeltaToolResult struct {
	ToolID   string
	ToolName string
	Result   ToolExecutionResult
}

func (DeltaToolResult) agentDeltaTag() {}

type DeltaStageMarker struct {
	Stage string
}

func (DeltaStageMarker) agentDeltaTag() {}

type DeltaSyntheticQuery struct {
	ChatID   string
	Role     string
	Message  string
	Messages []map[string]any
	System   map[string]any
	Kind     string
	Stage    string
	Hidden   bool
}

func (DeltaSyntheticQuery) agentDeltaTag() {}

type DeltaFinishReason struct {
	Reason string
}

func (DeltaFinishReason) agentDeltaTag() {}

type DeltaRunContinuation struct {
	SourceRunID string
	RunID       string
	ChatID      string
	AgentKey    string
	TeamID      string
	AwaitingID  string
	SubmitID    string
	Locale      string
	Mode        string
	Params      api.SubmitParams
	Answer      map[string]any
	// ContinuationState carries server-owned, in-memory admission state from
	// submit to the asynchronous continuation callback. It is never serialized.
	ContinuationState any
}

func (DeltaRunContinuation) agentDeltaTag() {}

type DeltaError struct {
	Error map[string]any
}

func (DeltaError) agentDeltaTag() {}

type DeltaPlanUpdate struct {
	PlanID string
	Plan   any
	ChatID string
}

func (DeltaPlanUpdate) agentDeltaTag() {}

type DeltaPlanningStart struct {
	PlanningID string
}

func (DeltaPlanningStart) agentDeltaTag() {}

type DeltaPlanningDelta struct {
	PlanningID string
	Delta      string
}

func (DeltaPlanningDelta) agentDeltaTag() {}

type DeltaPlanningEnd struct {
	PlanningID string
}

func (DeltaPlanningEnd) agentDeltaTag() {}

type DeltaTaskLifecycle struct {
	Kind         string
	TaskID       string
	RunID        string
	TaskName     string
	Description  string
	SubAgentKey  string
	MainToolID   string
	TeamID       string
	Presentation string
	Reason       string
	Error        map[string]any
}

func (DeltaTaskLifecycle) agentDeltaTag() {}

type SubAgentTaskSpec struct {
	SubAgentKey string
	TaskText    string
	TaskName    string
	Files       []string
}

type DeltaInvokeSubAgents struct {
	MainToolID string
	Tasks      []SubAgentTaskSpec
}

func (DeltaInvokeSubAgents) agentDeltaTag() {}

// DeltaTeamDispatch is emitted only by the hidden TEAM coordinator tools.
// The server owns member execution because it has the frozen TeamSnapshot,
// chat store, resource tickets and stream event bus.
type DeltaTeamDispatch struct {
	MainToolID   string
	Kind         string
	DelegateMode string
	Tasks        []SubAgentTaskSpec
}

func (DeltaTeamDispatch) agentDeltaTag() {}

type DeltaArtifactPublish struct {
	ChatID        string
	RunID         string
	ArtifactCount int
	Artifacts     []map[string]any
}

func (DeltaArtifactPublish) agentDeltaTag() {}

type DeltaSourcePublish struct {
	PublishID string
	RunID     string
	ToolID    string
	Kind      string
	Query     string
	Sources   []stream.Source
}

func (DeltaSourcePublish) agentDeltaTag() {}

type DeltaAwaitAsk struct {
	AwaitingID   string
	Mode         string
	Timeout      int64
	RunID        string
	ViewportType string
	ViewportKey  string
	Questions    []any
	Approvals    []any
	Forms        []any
	Plan         map[string]any
}

func (DeltaAwaitAsk) agentDeltaTag() {}

type DeltaRequestSubmit struct {
	RequestID  string
	ChatID     string
	RunID      string
	AwaitingID string
	SubmitID   string
	Params     any
}

func (DeltaRequestSubmit) agentDeltaTag() {}

type DeltaAwaitingAnswer struct {
	AwaitingID string
	// Answer carries the normalized awaiting.answer payload shape, which varies by mode/status.
	Answer map[string]any
}

func (DeltaAwaitingAnswer) agentDeltaTag() {}

type DeltaRequestSteer struct {
	RequestID string
	ChatID    string
	RunID     string
	SteerID   string
	Message   string
}

func (DeltaRequestSteer) agentDeltaTag() {}

type DeltaDebugLLMChat struct {
	TaskID                          string
	ChatID                          string
	ProviderKey                     string
	ProviderEndpoint                string
	ModelKey                        string
	ModelID                         string
	ReasoningEffort                 string
	Status                          string
	RunSeq                          int
	TraceFile                       string
	TraceURL                        string
	SystemRef                       map[string]any
	ContextWindow                   int
	CurrentContextSize              int
	EstimatedNextCallSize           int
	LLMReturnPromptTokens           int
	LLMReturnCompletionTokens       int
	LLMReturnTotalTokens            int
	LLMReturnCachedTokens           int
	LLMReturnReasoningTokens        int
	LLMReturnPromptCacheHitTokens   int
	LLMReturnPromptCacheMissTokens  int
	LLMReturnLLMChatCompletionCount int
	LLMReturnToolCallCount          int
	LLMReturnFirstTokenLatencyMs    int64
	LLMReturnGenerationDurationMs   int64
	RunPromptTokens                 int
	RunCompletionTokens             int
	RunTotalTokens                  int
	RunCachedTokens                 int
	RunReasoningTokens              int
	RunPromptCacheHitTokens         int
	RunPromptCacheMissTokens        int
	RunLLMChatCompletionCount       int
	RunToolCallCount                int
	RunFirstTokenLatencyTotalMs     int64
	RunFirstTokenLatencyCount       int
	RunGenerationDurationMs         int64
}

func (DeltaDebugLLMChat) agentDeltaTag() {}

type DeltaLLMRequest struct {
	TaskID          string
	ChatID          string
	Model           map[string]any
	ModelKey        string
	ReasoningEffort string
	System          map[string]any
	SystemRef       map[string]any
	ToolChoice      string
	RequestOptions  map[string]any
	InputMessages   []map[string]any
}

func (DeltaLLMRequest) agentDeltaTag() {}

type DeltaUsageSnapshot struct {
	ChatID                          string
	ModelKey                        string
	ReasoningEffort                 string
	ContextWindow                   int
	CurrentContextSize              int
	EstimatedNextCallSize           int
	LLMReturnPromptTokens           int
	LLMReturnCompletionTokens       int
	LLMReturnTotalTokens            int
	LLMReturnCachedTokens           int
	LLMReturnReasoningTokens        int
	LLMReturnPromptCacheHitTokens   int
	LLMReturnPromptCacheMissTokens  int
	LLMReturnLLMChatCompletionCount int
	LLMReturnToolCallCount          int
	LLMReturnFirstTokenLatencyMs    int64
	LLMReturnGenerationDurationMs   int64
	RunPromptTokens                 int
	RunCompletionTokens             int
	RunTotalTokens                  int
	RunCachedTokens                 int
	RunReasoningTokens              int
	RunPromptCacheHitTokens         int
	RunPromptCacheMissTokens        int
	RunLLMChatCompletionCount       int
	RunToolCallCount                int
	RunFirstTokenLatencyTotalMs     int64
	RunFirstTokenLatencyCount       int
	RunGenerationDurationMs         int64
}

func (DeltaUsageSnapshot) agentDeltaTag() {}

type DeltaRunActivity struct {
	TaskID      string
	ChatID      string
	Phase       string
	Status      string
	Backend     string
	Key         string
	Message     string
	Retry       map[string]any
	Recovery    map[string]any
	Degradation map[string]any
}

func (DeltaRunActivity) agentDeltaTag() {}

type DeltaRunCancel struct {
	RunID string
}

func (DeltaRunCancel) agentDeltaTag() {}
