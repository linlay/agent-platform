package contracts

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
	ToolIDs []string
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

type DeltaFinishReason struct {
	Reason string
}

func (DeltaFinishReason) agentDeltaTag() {}

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
	PlanningID   string
	PlanningFile string
	ChatID       string
	RunID        string
	RequestID    string
	AgentKey     string
	Title        string
	Status       string
}

func (DeltaPlanningStart) agentDeltaTag() {}

type DeltaPlanningDelta struct {
	PlanningID   string
	PlanningFile string
	ChatID       string
	RunID        string
	RequestID    string
	AgentKey     string
	Title        string
	Status       string
	Delta        string
}

func (DeltaPlanningDelta) agentDeltaTag() {}

type DeltaPlanningSnapshot struct {
	PlanningID   string
	PlanningFile string
	ChatID       string
	RunID        string
	RequestID    string
	AgentKey     string
	Title        string
	Status       string
	Markdown     string
}

func (DeltaPlanningSnapshot) agentDeltaTag() {}

type DeltaPlanningEnd struct {
	PlanningID   string
	PlanningFile string
	ChatID       string
	RunID        string
	RequestID    string
	AgentKey     string
	Title        string
	Status       string
	Markdown     string
}

func (DeltaPlanningEnd) agentDeltaTag() {}

type DeltaTaskLifecycle struct {
	Kind        string
	TaskID      string
	RunID       string
	TaskName    string
	Description string
	SubAgentKey string
	MainToolID  string
	Reason      string
	Error       map[string]any
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

type DeltaArtifactPublish struct {
	ChatID        string
	RunID         string
	ArtifactCount int
	Artifacts     []map[string]any
}

func (DeltaArtifactPublish) agentDeltaTag() {}

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
}

func (DeltaAwaitAsk) agentDeltaTag() {}

type DeltaRequestSubmit struct {
	RequestID  string
	ChatID     string
	RunID      string
	AwaitingID string
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

type DeltaDebugPreCall struct {
	ChatID                    string
	ProviderKey               string
	ProviderEndpoint          string
	ModelKey                  string
	ModelID                   string
	RequestBody               map[string]any
	InjectedPrompt            map[string]any
	SystemRef                 map[string]any
	ContextWindow             int
	CurrentContextSize        int
	EstimatedNextCallSize     int
	RunPromptTokens           int
	RunCompletionTokens       int
	RunTotalTokens            int
	RunCachedTokens           int
	RunReasoningTokens        int
	RunPromptCacheHitTokens   int
	RunPromptCacheMissTokens  int
	RunLLMChatCompletionCount int
}

func (DeltaDebugPreCall) agentDeltaTag() {}

type DeltaDebugPostCall struct {
	ChatID                          string
	ModelKey                        string
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
	RunPromptTokens                 int
	RunCompletionTokens             int
	RunTotalTokens                  int
	RunCachedTokens                 int
	RunReasoningTokens              int
	RunPromptCacheHitTokens         int
	RunPromptCacheMissTokens        int
	RunLLMChatCompletionCount       int
}

func (DeltaDebugPostCall) agentDeltaTag() {}

type DeltaUsageSnapshot struct {
	ChatID                          string
	ModelKey                        string
	ContextWindow                   int
	CurrentContextSize              int
	EstimatedNextCallSize           int
	CacheDiagnostics                map[string]any
	LLMReturnPromptTokens           int
	LLMReturnCompletionTokens       int
	LLMReturnTotalTokens            int
	LLMReturnCachedTokens           int
	LLMReturnReasoningTokens        int
	LLMReturnPromptCacheHitTokens   int
	LLMReturnPromptCacheMissTokens  int
	LLMReturnLLMChatCompletionCount int
	RunPromptTokens                 int
	RunCompletionTokens             int
	RunTotalTokens                  int
	RunCachedTokens                 int
	RunReasoningTokens              int
	RunPromptCacheHitTokens         int
	RunPromptCacheMissTokens        int
	RunLLMChatCompletionCount       int
}

func (DeltaUsageSnapshot) agentDeltaTag() {}

type DeltaRunCancel struct {
	RunID string
}

func (DeltaRunCancel) agentDeltaTag() {}
