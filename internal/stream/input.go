package stream

type StreamInput interface {
	streamInputTag()
}

type ReasoningDelta struct {
	ReasoningID string
	Delta       string
	TaskID      string
}

func (ReasoningDelta) streamInputTag() {}

type ContentDelta struct {
	ContentID string
	Delta     string
	TaskID    string
}

func (ContentDelta) streamInputTag() {}

type ToolArgs struct {
	ToolID          string
	Delta           string
	TaskID          string
	ToolName        string
	ToolType        string
	ToolLabel       string
	ToolDescription string
	ChunkIndex      int
}

func (ToolArgs) streamInputTag() {}

type ToolEnd struct {
	ToolID string
}

func (ToolEnd) streamInputTag() {}

type ToolResult struct {
	ToolID          string
	ToolName        string
	ToolType        string
	ToolLabel       string
	ToolDescription string
	Result          any
	Error           string
	ExitCode        int
}

func (ToolResult) streamInputTag() {}

type ActionArgs struct {
	ActionID    string
	Delta       string
	TaskID      string
	ActionName  string
	Description string
}

func (ActionArgs) streamInputTag() {}

type ActionEnd struct {
	ActionID string
}

func (ActionEnd) streamInputTag() {}

type ActionResult struct {
	ActionID    string
	ActionName  string
	Description string
	Result      any
}

func (ActionResult) streamInputTag() {}

type PlanUpdate struct {
	PlanID string
	Plan   any
	ChatID string
}

func (PlanUpdate) streamInputTag() {}

type TaskStart struct {
	TaskID      string
	RunID       string
	TaskName    string
	Description string
}

func (TaskStart) streamInputTag() {}

type TaskComplete struct {
	TaskID string
}

func (TaskComplete) streamInputTag() {}

type TaskCancel struct {
	TaskID string
}

func (TaskCancel) streamInputTag() {}

type TaskFail struct {
	TaskID string
	Error  map[string]any
}

func (TaskFail) streamInputTag() {}

type ArtifactPublish struct {
	ArtifactID string
	ChatID     string
	RunID      string
	Artifact   any
}

func (ArtifactPublish) streamInputTag() {}

type RequestSubmit struct {
	RequestID string
	ChatID    string
	RunID     string
	ToolID    string
	Payload   any
	ViewID    string
}

func (RequestSubmit) streamInputTag() {}

type RequestSteer struct {
	RequestID string
	ChatID    string
	RunID     string
	SteerID   string
	Message   string
}

func (RequestSteer) streamInputTag() {}

type RunCancel struct {
	RunID string
}

func (RunCancel) streamInputTag() {}

type InputRunComplete struct {
	FinishReason string
}

func (InputRunComplete) streamInputTag() {}

type InputRunError struct {
	Error map[string]any
}

func (InputRunError) streamInputTag() {}
