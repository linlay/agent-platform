package llm

import (
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestAccumulateUsageIgnoresEmptyProviderUsage(t *testing.T) {
	stream := &llmRunStream{
		session:                        contracts.QuerySession{RunID: "run-empty-usage", ChatID: "chat-empty-usage"},
		model:                          models.ModelDefinition{Key: "mock-model", ContextWindow: 128000},
		currentTurn:                    &providerTurnStream{},
		runLLMChatCompletionCount:      1,
		lastCallLLMChatCompletionCount: 1,
	}

	stream.accumulateUsage(&openAIUsage{})
	stream.accumulateUsage(&openAIUsage{})
	stream.emitPendingUsageDelta()

	if len(stream.pending) != 0 {
		t.Fatalf("did not expect deltas for empty provider usage, got %#v", stream.pending)
	}
	if stream.runPromptTokens != 0 || stream.runCompletionTokens != 0 || stream.runTotalTokens != 0 {
		t.Fatalf("did not expect empty provider usage to update run usage: prompt=%d completion=%d total=%d", stream.runPromptTokens, stream.runCompletionTokens, stream.runTotalTokens)
	}
}

func TestAccumulateUsageCommitsLatestValidProviderUsageOnce(t *testing.T) {
	stream := &llmRunStream{
		session:                        contracts.QuerySession{RunID: "run-valid-usage", ChatID: "chat-valid-usage"},
		model:                          models.ModelDefinition{Key: "mock-model", ContextWindow: 128000},
		currentTurn:                    &providerTurnStream{},
		runLLMChatCompletionCount:      1,
		lastCallLLMChatCompletionCount: 1,
	}

	stream.accumulateUsage(&openAIUsage{
		PromptTokens:     10,
		CompletionTokens: 2,
		TotalTokens:      12,
	})
	stream.accumulateUsage(&openAIUsage{
		PromptTokens:          20,
		CompletionTokens:      5,
		TotalTokens:           25,
		PromptCacheHitTokens:  7,
		PromptCacheMissTokens: 13,
	})
	stream.emitPendingUsageDelta()
	stream.emitPendingUsageDelta()

	if stream.runPromptTokens != 20 || stream.runCompletionTokens != 5 || stream.runTotalTokens != 25 {
		t.Fatalf("expected latest usage to be committed once, got prompt=%d completion=%d total=%d", stream.runPromptTokens, stream.runCompletionTokens, stream.runTotalTokens)
	}
	if len(stream.pending) != 2 {
		t.Fatalf("expected usage.snapshot and debug.postCall deltas, got %#v", stream.pending)
	}
	snapshot, ok := stream.pending[0].(contracts.DeltaUsageSnapshot)
	if !ok {
		t.Fatalf("expected first delta to be DeltaUsageSnapshot, got %#v", stream.pending[0])
	}
	if snapshot.LLMReturnPromptTokens != 20 || snapshot.LLMReturnCompletionTokens != 5 || snapshot.LLMReturnTotalTokens != 25 ||
		snapshot.LLMReturnPromptCacheHitTokens != 7 || snapshot.LLMReturnPromptCacheMissTokens != 13 {
		t.Fatalf("unexpected usage snapshot %#v", snapshot)
	}
	if _, ok := stream.pending[1].(contracts.DeltaDebugPostCall); !ok {
		t.Fatalf("expected second delta to be DeltaDebugPostCall, got %#v", stream.pending[1])
	}
}
