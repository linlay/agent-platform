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
	ContentID    string
	Delta        string
	TaskID       string
	ActorType    string
	TeamID       string
	AgentKey     string
	Presentation string
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
	ToolID     string
	FileChange map[string]any
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
	FileChange      map[string]any
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

type SyntheticQuery struct {
	ChatID   string
	Role     string
	Message  string
	Messages []map[string]any
	System   map[string]any
	Kind     string
	Stage    string
	Hidden   bool
}

func (SyntheticQuery) streamInputTag() {}

type PlanUpdate struct {
	PlanID string
	Plan   any
	ChatID string
}

func (PlanUpdate) streamInputTag() {}

type PlanningStart struct {
	PlanningID string
}

func (PlanningStart) streamInputTag() {}

type PlanningDelta struct {
	PlanningID string
	Delta      string
}

func (PlanningDelta) streamInputTag() {}

type PlanningEnd struct {
	PlanningID string
}

func (PlanningEnd) streamInputTag() {}

type TaskStart struct {
	TaskID       string
	RunID        string
	TaskName     string
	Description  string
	SubAgentKey  string
	MainToolID   string
	TeamID       string
	Presentation string
}

func (TaskStart) streamInputTag() {}

type TaskComplete struct {
	TaskID       string
	TeamID       string
	AgentKey     string
	Presentation string
}

func (TaskComplete) streamInputTag() {}

type TaskCancel struct {
	TaskID       string
	Reason       string
	TeamID       string
	AgentKey     string
	Presentation string
}

func (TaskCancel) streamInputTag() {}

type TaskError struct {
	TaskID       string
	Error        map[string]any
	TeamID       string
	AgentKey     string
	Presentation string
}

func (TaskError) streamInputTag() {}

type ArtifactPublish struct {
	ChatID        string
	RunID         string
	ArtifactCount int
	Artifacts     []map[string]any
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
	ChunkID    string  `json:"chunkId"`
	Index      int     `json:"index"`
	Content    string  `json:"content"`
	Score      float64 `json:"score,omitempty"`
	Timestamp  *int64  `json:"timestamp,omitempty"`
	Path       string  `json:"path,omitempty"`
	Heading    string  `json:"heading,omitempty"`
	StartLine  int     `json:"startLine,omitempty"`
	EndLine    int     `json:"endLine,omitempty"`
	PageStart  int     `json:"pageStart,omitempty"`
	PageEnd    int     `json:"pageEnd,omitempty"`
	SlideStart int     `json:"slideStart,omitempty"`
	SlideEnd   int     `json:"slideEnd,omitempty"`
	SourceType string  `json:"sourceType,omitempty"`
	MatchType  string  `json:"matchType,omitempty"`
}

type AwaitAsk struct {
	AwaitingID   string
	Mode         string
	Timeout      int64
	RunID        string
	TaskID       string
	ViewportType string
	ViewportKey  string
	Questions    []any
	Approvals    []any
	Forms        []any
	Plan         map[string]any
}

func (AwaitAsk) streamInputTag() {}

type RequestSubmit struct {
	RequestID  string
	ChatID     string
	RunID      string
	TaskID     string
	AwaitingID string
	SubmitID   string
	Params     any
}

func (RequestSubmit) streamInputTag() {}

type AwaitingAnswer struct {
	AwaitingID string
	TaskID     string
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

type InputDebugLLMChat struct {
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

func (InputDebugLLMChat) streamInputTag() {}

type InputLLMRequest struct {
	TaskID          string
	ChatID          string
	ActorType       string
	TeamID          string
	AgentKey        string
	Presentation    string
	Model           map[string]any
	ModelKey        string
	ReasoningEffort string
	System          map[string]any
	SystemRef       map[string]any
	ToolChoice      string
	RequestOptions  map[string]any
	InputMessages   []map[string]any
}

func (InputLLMRequest) streamInputTag() {}

type InputUsageSnapshot struct {
	TaskID                          string
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

func (InputUsageSnapshot) streamInputTag() {}

type InputRunActivity struct {
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

func (InputRunActivity) streamInputTag() {}

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
