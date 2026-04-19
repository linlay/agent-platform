package stream

type StreamEventStateData struct {
	activeReasoningID string
	activeReasoning   reasoningBlockState
	activeContentID   string
	activeContent     contentBlockState
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
	runFinishReason   string
	runError          map[string]any
	runUsage          *runUsageState
	terminated        bool
}

type runUsageState struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type reasoningBlockState struct {
	TaskID string
	Label  string
}

type contentBlockState struct {
	TaskID string
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
		openTools:        map[string]toolBlockState{},
		openActions:      map[string]actionBlockState{},
		reasoningBuffer:  map[string]string{},
		contentBuffer:    map[string]string{},
		toolArgsBuffer:   map[string]string{},
		actionArgsBuffer: map[string]string{},
		emittedAwaitings: map[string]bool{},
	}
}
