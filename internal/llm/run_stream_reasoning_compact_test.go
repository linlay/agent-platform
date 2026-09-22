package llm

import (
	"agent-platform/internal/models"
	"strings"
	"testing"
)

func TestReasoningAloneDoesNotTriggerBelowContextThreshold(t *testing.T) {
	s := &llmRunStream{model: models.ModelDefinition{ContextWindow: 200000}, messages: []openAIMessage{{Role: "assistant", ReasoningContent: strings.Repeat("r", 32000)}}}
	if s.scheduleContextCompact(false) {
		t.Fatal("reasoning must share the 90 percent trigger")
	}
}

func TestReasoningOnlyOutputDoesNotInflateInputCalibration(t *testing.T) {
	s := &llmRunStream{model: models.ModelDefinition{ContextWindow: 200000}, messages: []openAIMessage{{Role: "system", Content: strings.Repeat("s", 24000)}, {Role: "user", Content: strings.Repeat("h", 2000)}, {Role: "user", Content: "create skill"}}, pinnedMessageStart: 2, pinnedMessageEnd: 3, lastCallUseProjectedContext: true}
	s.lastRequestRawTokens = estimateModelContext(s.messages, nil)
	s.commitUsage(&openAIUsage{PromptTokens: 7809, CompletionTokens: 192191, TotalTokens: 200000})
	if s.compactEstimateScale > 2 || s.estimatedNextCallSize() > 8000 {
		t.Fatalf("reasoning charged as context: scale=%f tokens=%d", s.compactEstimateScale, s.estimatedNextCallSize())
	}
	if s.scheduleContextCompact(true) {
		t.Fatal("dropped output triggered compaction")
	}
	if s.lastCallCompletionTokens != 192191 || s.runCompletionTokens != 192191 {
		t.Fatal("billing usage altered")
	}
}
