package stream

type StreamEventStateData struct {
	activeReasoningID string
	activeContentID   string
	planID            string
	activeTaskID      string
	openTools         map[string]toolBlockState
	openActions       map[string]actionBlockState
	contentSeen       bool
	lastContentID     string
	fullContent       string
	reasoningBuffer   map[string]string
	toolArgsBuffer    map[string]string
	runFinishReason   string
	runError          map[string]any
	terminated        bool
}

type toolBlockState struct {
	Name        string
	Type        string
	Label       string
	Description string
}

type actionBlockState struct {
	Name        string
	Description string
}

func NewStateData() *StreamEventStateData {
	return &StreamEventStateData{
		openTools:       map[string]toolBlockState{},
		openActions:     map[string]actionBlockState{},
		reasoningBuffer: map[string]string{},
		toolArgsBuffer:  map[string]string{},
	}
}
