// Package modelusage normalizes provider usage payloads of auxiliary model
// calls (tools calling image, vision or speech models) into contracts.ModelUsage.
// It records only what the provider reports and never estimates missing values.
package modelusage

import (
	"strings"

	"agent-platform/internal/contracts"
)

const (
	OperationImageGeneration = "image.generation"
	OperationImageEdit       = "image.edit"
	OperationImageInpainting = "image.inpainting"
	OperationVision          = "vision.recognize"
	OperationSpeech          = "speech.generation"
)

// FromProvider converts a raw provider usage object. It accepts the Chat
// Completions shape (prompt_tokens/completion_tokens), the Anthropic shape
// (input_tokens plus cache_read/cache_creation_input_tokens) and the OpenAI
// Images/Responses shape (input_tokens/output_tokens with input_tokens_details).
// ok is false when the payload reports no tokens.
func FromProvider(modelKey string, operation string, raw map[string]any) (contracts.ModelUsage, bool) {
	usage := contracts.ModelUsage{
		ModelKey:  strings.TrimSpace(modelKey),
		Operation: strings.TrimSpace(operation),
	}
	if len(raw) == 0 {
		return usage, false
	}
	switch {
	case hasAnyKey(raw, "prompt_tokens", "completion_tokens"):
		usage.PromptTokens = contracts.AnyIntNode(raw["prompt_tokens"])
		usage.CompletionTokens = contracts.AnyIntNode(raw["completion_tokens"])
		usage.ReasoningTokens = detailInt(raw, "completion_tokens_details", "reasoning_tokens")
		usage.CacheHitTokens = firstPositive(
			contracts.AnyIntNode(raw["prompt_cache_hit_tokens"]),
			detailInt(raw, "prompt_tokens_details", "cached_tokens"),
		)
		usage.CacheMissTokens = contracts.AnyIntNode(raw["prompt_cache_miss_tokens"])
	case hasAnyKey(raw, "cache_read_input_tokens", "cache_creation_input_tokens"):
		// Anthropic input_tokens excludes cache reads and writes.
		input := contracts.AnyIntNode(raw["input_tokens"])
		cacheWrite := contracts.AnyIntNode(raw["cache_creation_input_tokens"])
		cacheRead := contracts.AnyIntNode(raw["cache_read_input_tokens"])
		usage.PromptTokens = input + cacheWrite + cacheRead
		usage.CompletionTokens = contracts.AnyIntNode(raw["output_tokens"])
		usage.CacheHitTokens = cacheRead
		usage.CacheMissTokens = input + cacheWrite
	default:
		usage.PromptTokens = contracts.AnyIntNode(raw["input_tokens"])
		usage.CompletionTokens = contracts.AnyIntNode(raw["output_tokens"])
		usage.ReasoningTokens = detailInt(raw, "output_tokens_details", "reasoning_tokens")
		usage.CacheHitTokens = detailInt(raw, "input_tokens_details", "cached_tokens")
	}
	usage.TotalTokens = contracts.AnyIntNode(raw["total_tokens"])
	if usage.TotalTokens <= 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.CacheMissTokens <= 0 && usage.CacheHitTokens > 0 && usage.PromptTokens > usage.CacheHitTokens {
		usage.CacheMissTokens = usage.PromptTokens - usage.CacheHitTokens
	}
	return usage, usage.HasTokens()
}

// Attach records the provider usage on a tool result when it reports tokens.
func Attach(result *contracts.ToolExecutionResult, modelKey string, operation string, raw map[string]any) {
	if result == nil {
		return
	}
	if usage, ok := FromProvider(modelKey, operation, raw); ok {
		result.ModelUsage = append(result.ModelUsage, usage)
	}
}

// AttachCharacters records a call billed per character when the provider
// reported a billed character count. Callers pass only the provider's own
// figure, never a locally counted text length.
func AttachCharacters(result *contracts.ToolExecutionResult, modelKey string, operation string, characters int, upstreamRequestID string) {
	if result == nil || characters <= 0 {
		return
	}
	result.ModelUsage = append(result.ModelUsage, contracts.ModelUsage{
		ModelKey:          strings.TrimSpace(modelKey),
		Operation:         strings.TrimSpace(operation),
		Characters:        characters,
		UpstreamRequestID: strings.TrimSpace(upstreamRequestID),
	})
}

func hasAnyKey(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func detailInt(values map[string]any, detailKey string, valueKey string) int {
	return contracts.AnyIntNode(contracts.AnyMapNode(values[detailKey])[valueKey])
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
