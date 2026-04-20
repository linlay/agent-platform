package stream

type StreamInput interface {
	streamInputTag()
}

type ReasoningDelta struct {
	ReasoningID    string
	ReasoningLabel string
	Delta          string
	TaskID         string
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
	ToolLabel       string
	ToolDescription string
	ChunkIndex      int
	AwaitAsk        *AwaitAsk
}

func (ToolArgs) streamInputTag() {}

type ToolEnd struct {
	ToolID string
}

func (ToolEnd) streamInputTag() {}

type ToolResult struct {
	ToolID          string
	ToolName        string
	ToolLabel       string
	ToolDescription string
	Result          any
	Hitl            map[string]any
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

type StageMarker struct {
	Stage string
}

func (StageMarker) streamInputTag() {}

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
	SubAgentKey string
	MainToolID  string
}

func (TaskStart) streamInputTag() {}

type TaskComplete struct {
	TaskID string
	Status string
}

func (TaskComplete) streamInputTag() {}

type TaskCancel struct {
	TaskID string
	Status string
}

func (TaskCancel) streamInputTag() {}

type TaskFail struct {
	TaskID string
	Status string
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

type SourcePublish struct {
	PublishID   string
	RunID       string
	TaskID      string
	ToolID      string
	Kind        string
	Query       string
	SourceCount int
	ChunkCount  int
	Sources     []Source
}

func (SourcePublish) streamInputTag() {}

type Source struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Title          string        `json:"title,omitempty"`
	Icon           string        `json:"icon,omitempty"`
	URL            string        `json:"url,omitempty"`
	Link           string        `json:"link,omitempty"`
	CollectionID   string        `json:"collectionId,omitempty"`
	CollectionName string        `json:"collectionName,omitempty"`
	ChunkIndexes   []int         `json:"chunkIndexes"`
	MinIndex       int           `json:"minIndex"`
	Chunks         []SourceChunk `json:"chunks"`
}

type SourceChunk struct {
	ChunkID   string  `json:"chunkId"`
	Index     int     `json:"index"`
	Content   string  `json:"content"`
	Score     float64 `json:"score,omitempty"`
	Timestamp *int64  `json:"timestamp,omitempty"`
}

type AwaitAsk struct {
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

func (AwaitAsk) streamInputTag() {}

type RequestSubmit struct {
	RequestID  string
	ChatID     string
	RunID      string
	AwaitingID string
	Params     any
}

func (RequestSubmit) streamInputTag() {}

type AwaitingAnswer struct {
	AwaitingID string
	Answer     map[string]any
}

func (AwaitingAnswer) streamInputTag() {}

type RequestSteer struct {
	RequestID string
	ChatID    string
	RunID     string
	SteerID   string
	Message   string
}

func (RequestSteer) streamInputTag() {}

type InputDebugPreCall struct {
	ChatID                string
	ModelKey              string
	ContextWindow         int
	CurrentContextSize    int
	EstimatedNextCallSize int
	RunPromptTokens       int
	RunCompletionTokens   int
	RunTotalTokens        int
}

func (InputDebugPreCall) streamInputTag() {}

type InputDebugPostCall struct {
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

func (InputDebugPostCall) streamInputTag() {}

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
