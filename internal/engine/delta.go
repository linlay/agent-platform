package engine

type AgentDelta interface {
	agentDeltaTag()
}

type DeltaContent struct {
	Text      string
	ContentID string
}

func (DeltaContent) agentDeltaTag() {}

type DeltaReasoning struct {
	Text        string
	ReasoningID string
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
	TaskName    string
	Description string
	Error       map[string]any
}

func (DeltaTaskLifecycle) agentDeltaTag() {}

type DeltaArtifactPublish struct {
	ArtifactID string
	ChatID     string
	RunID      string
	Artifact   any
}

func (DeltaArtifactPublish) agentDeltaTag() {}

type DeltaRequestSubmit struct {
	RequestID string
	ChatID    string
	RunID     string
	ToolID    string
	Payload   any
	ViewID    string
}

func (DeltaRequestSubmit) agentDeltaTag() {}

type DeltaRequestSteer struct {
	RequestID string
	ChatID    string
	RunID     string
	SteerID   string
	Message   string
}

func (DeltaRequestSteer) agentDeltaTag() {}

type DeltaRunCancel struct {
	RunID string
}

func (DeltaRunCancel) agentDeltaTag() {}
