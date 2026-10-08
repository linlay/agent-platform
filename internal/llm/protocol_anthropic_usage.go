package llm

import . "agent-platform/internal/contracts"

// Anthropic splits input usage across uncached, cache-write and cache-read
// tokens. message_delta usage is cumulative and may omit message_start fields.
func mergeAnthropicUsage(previous *openAIUsage, update map[string]any) *openAIUsage {
	if len(update) == 0 {
		return nil
	}
	var raw map[string]any
	if previous != nil {
		raw = previous.Raw
	}
	raw = mergeAnyMaps(raw, update)
	input := AnyIntNode(raw["input_tokens"])
	cacheWrite := AnyIntNode(raw["cache_creation_input_tokens"])
	cacheRead := AnyIntNode(raw["cache_read_input_tokens"])
	output := AnyIntNode(raw["output_tokens"])
	prompt := input + cacheWrite + cacheRead
	details := AnyMapNode(raw["output_tokens_details"])
	return &openAIUsage{
		PromptTokens:            prompt,
		CompletionTokens:        output,
		TotalTokens:             prompt + output,
		PromptTokensDetails:     openAIPromptTokenDetails{CachedTokens: cacheRead},
		CompletionTokensDetails: openAICompletionTokenDetails{ReasoningTokens: AnyIntNode(details["thinking_tokens"])},
		PromptCacheHitTokens:    cacheRead,
		PromptCacheMissTokens:   input + cacheWrite,
		Raw:                     raw,
	}
}
