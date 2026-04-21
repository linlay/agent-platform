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

type DeltaTaskLifecycle struct {
	Kind        string
	TaskID      string
	RunID       string
	GroupID     string
	TaskName    string
	Description string
	SubAgentKey string
	MainToolID  string
	Status      string
	Error       map[string]any
}

func (DeltaTaskLifecycle) agentDeltaTag() {}

type SubAgentTaskSpec struct {
	SubAgentKey string
	TaskText    string
	TaskName    string
}

type DeltaInvokeSubAgents struct {
	MainToolID string
	GroupID    string
	Tasks      []SubAgentTaskSpec
}

func (DeltaInvokeSubAgents) agentDeltaTag() {}

type DeltaArtifactPublish struct {
	ArtifactID string
	ChatID     string
	RunID      string
	Artifact   any
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
	ChatID                string
	ModelKey              string
	ContextWindow         int
	CurrentContextSize    int
	EstimatedNextCallSize int
	RunPromptTokens       int
	RunCompletionTokens   int
	RunTotalTokens        int
}

func (DeltaDebugPreCall) agentDeltaTag() {}

type DeltaDebugPostCall struct {
	ChatID                    string
	ModelKey                  string
	ContextWindow             int
	CurrentContextSize        int
	EstimatedNextCallSize     int
	LLMReturnPromptTokens     int
	LLMReturnCompletionTokens int
	LLMReturnTotalTokens      int
	RunPromptTokens           int
	RunCompletionTokens       int
	RunTotalTokens            int
}

func (DeltaDebugPostCall) agentDeltaTag() {}

type DeltaRunCancel struct {
	RunID string
}

func (DeltaRunCancel) agentDeltaTag() {}
