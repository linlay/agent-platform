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
		session:     contracts.QuerySession{RunID: "run-valid-usage", ChatID: "chat-valid-usage"},
		model:       models.ModelDefinition{Key: "mock-model", ContextWindow: 128000},
		currentTurn: &providerTurnStream{},
		messages: []openAIMessage{
			{Role: "system", Content: "stable system prompt"},
			{Role: "user", Content: "write an article"},
			{Role: "assistant", Content: "previous long assistant answer"},
			{Role: "user", Content: "change topic"},
		},
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
	if snapshot.CacheDiagnostics == nil || snapshot.CacheDiagnostics["cacheMissTokens"] != 13 {
		t.Fatalf("expected cache diagnostics in usage snapshot, got %#v", snapshot.CacheDiagnostics)
	}
	if _, ok := stream.pending[1].(contracts.DeltaDebugPostCall); !ok {
		t.Fatalf("expected second delta to be DeltaDebugPostCall, got %#v", stream.pending[1])
	}
}

func TestNormalizeOpenAIUsageMapsCachedTokensAsPromptCacheHitTokens(t *testing.T) {
	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens: 100,
		PromptTokensDetails: openAIPromptTokenDetails{
			CachedTokens: 40,
		},
	}, protocolRuntimeConfig{})

	if normalized.CacheHitTokens != 40 || normalized.CacheMissTokens != 60 {
		t.Fatalf("expected cached_tokens to normalize with derived cache miss, got %#v", normalized)
	}
}

func TestNormalizeOpenAIUsageInfersPromptCacheMissTokensByCompat(t *testing.T) {
	protocolConfig := protocolRuntimeConfig{
		Compat: map[string]any{
			"response": map[string]any{
				"usage": map[string]any{
					"promptTokensDetails": map[string]any{
						"cacheMissTokens": map[string]any{
							"derive": "promptTokensMinusCacheHitTokens",
						},
					},
				},
			},
		},
	}

	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens: 100,
		PromptTokensDetails: openAIPromptTokenDetails{
			CachedTokens: 40,
		},
	}, protocolConfig)

	if normalized.CacheHitTokens != 40 || normalized.CacheMissTokens != 60 {
		t.Fatalf("expected cache miss to be inferred from prompt minus cached tokens, got %#v", normalized)
	}
}

func TestNormalizeOpenAIUsageMapsDeepSeekCacheUsageByCompatPath(t *testing.T) {
	protocolConfig := protocolRuntimeConfig{
		Compat: map[string]any{
			"response": map[string]any{
				"usage": map[string]any{
					"promptTokensDetails": map[string]any{
						"cacheHitTokens": map[string]any{
							"path": "prompt_cache_hit_tokens",
						},
						"cacheMissTokens": map[string]any{
							"path": "prompt_cache_miss_tokens",
						},
					},
					"completionTokensDetails": map[string]any{
						"reasoningTokens": map[string]any{
							"path": "completion_tokens_details.reasoning_tokens",
						},
					},
				},
			},
		},
	}

	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens: 100,
		Raw: map[string]any{
			"prompt_cache_hit_tokens":  32,
			"prompt_cache_miss_tokens": 68,
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 11,
			},
		},
	}, protocolConfig)

	if normalized.CacheHitTokens != 32 || normalized.CacheMissTokens != 68 || normalized.ReasoningTokens != 11 {
		t.Fatalf("expected deepseek usage mapping, got %#v", normalized)
	}
}

func TestNormalizeOpenAIUsageMapsMiMoCacheUsageByCompatPathAndDerive(t *testing.T) {
	protocolConfig := protocolRuntimeConfig{
		Compat: map[string]any{
			"response": map[string]any{
				"usage": map[string]any{
					"promptTokensDetails": map[string]any{
						"cacheHitTokens": map[string]any{
							"path": "prompt_tokens_details.cached_tokens",
						},
						"cacheMissTokens": map[string]any{
							"derive": "promptTokensMinusCacheHitTokens",
						},
					},
					"completionTokensDetails": map[string]any{
						"reasoningTokens": map[string]any{
							"path": "completion_tokens_details.reasoning_tokens",
						},
					},
				},
			},
		},
	}

	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens: 100,
		Raw: map[string]any{
			"prompt_tokens_details": map[string]any{
				"cached_tokens": 32,
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 11,
			},
		},
	}, protocolConfig)

	if normalized.CacheHitTokens != 32 || normalized.CacheMissTokens != 68 || normalized.ReasoningTokens != 11 {
		t.Fatalf("expected mimo usage mapping with derived miss tokens, got %#v", normalized)
	}
}

func TestNormalizeOpenAIUsageMapsMiniMaxCacheUsageByCompatPathAndDerive(t *testing.T) {
	protocolConfig := protocolRuntimeConfig{
		Compat: map[string]any{
			"response": map[string]any{
				"usage": map[string]any{
					"promptTokensDetails": map[string]any{
						"cacheHitTokens": map[string]any{
							"path": "prompt_tokens_details.cached_tokens",
						},
						"cacheMissTokens": map[string]any{
							"derive": "promptTokensMinusCacheHitTokens",
						},
					},
				},
			},
		},
	}

	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens: 5233,
		Raw: map[string]any{
			"prompt_tokens_details": map[string]any{
				"cached_tokens": 4475,
			},
		},
	}, protocolConfig)

	if normalized.CacheHitTokens != 4475 || normalized.CacheMissTokens != 758 {
		t.Fatalf("expected minimax usage mapping with derived miss tokens, got %#v", normalized)
	}
}

func TestNormalizeOpenAIUsageFallsBackCachedTokensFromExplicitPromptCacheHitTokens(t *testing.T) {
	normalized := normalizeOpenAIUsage(&openAIUsage{
		PromptTokens:          100,
		PromptCacheHitTokens:  25,
		PromptCacheMissTokens: 75,
	}, protocolRuntimeConfig{})

	if normalized.CacheHitTokens != 25 || normalized.CacheMissTokens != 75 {
		t.Fatalf("expected explicit prompt cache fields to normalize, got %#v", normalized)
	}
}
