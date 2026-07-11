package stream

type StreamEventStateData struct {
	activeReasonings  map[string]activeReasoningState
	activeContents    map[string]activeContentState
	planID            string
	activeTaskID      string
	openTools         map[string]toolBlockState
	openActions       map[string]actionBlockState
	contentSeen       bool
	lastContentID     string
	fullContent       string
	reasoningSeen     bool
	lastReasoningID   string
	fullReasoning     string
	reasoningBuffer   map[string]string
	contentBuffer     map[string]string
	toolArgsBuffer    map[string]string
	actionArgsBuffer  map[string]string
	emittedAwaitings  map[string]bool
	toolEndAtByID     map[string]int64
	awaitingAskAtByID map[string]int64
	runFinishReason   string
	runError          map[string]any
	runUsage          *runUsageState
	terminated        bool
}

type runUsageState struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CachedTokens             int
	ReasoningTokens          int
	PromptCacheHitTokens     int
	PromptCacheMissTokens    int
	LLMChatCompletionCount   int
	ToolCallCount            int
	FirstTokenLatencyTotalMs int64
	FirstTokenLatencyCount   int
	GenerationDurationMs     int64
}

type reasoningBlockState struct {
	TaskID string
	Label  string
}

type activeReasoningState struct {
	ID    string
	Block reasoningBlockState
}

type contentBlockState struct {
	TaskID       string
	ActorType    string
	TeamID       string
	AgentKey     string
	Presentation string
}

type activeContentState struct {
	ID    string
	Block contentBlockState
}

type toolBlockState struct {
	TaskID      string
	Name        string
	Label       string
	Description string
}

type actionBlockState struct {
	TaskID      string
	Name        string
	Description string
}

func NewStateData() *StreamEventStateData {
	return &StreamEventStateData{
		activeReasonings:  map[string]activeReasoningState{},
		activeContents:    map[string]activeContentState{},
		openTools:         map[string]toolBlockState{},
		openActions:       map[string]actionBlockState{},
		reasoningBuffer:   map[string]string{},
		contentBuffer:     map[string]string{},
		toolArgsBuffer:    map[string]string{},
		actionArgsBuffer:  map[string]string{},
		emittedAwaitings:  map[string]bool{},
		toolEndAtByID:     map[string]int64{},
		awaitingAskAtByID: map[string]int64{},
	}
}
