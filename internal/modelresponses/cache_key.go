package modelresponses

import (
	"crypto/sha256"
	"encoding/hex"
)

// PromptCacheKey is derived only from the Chat identity, so retries, later Runs
// and restored sessions reuse it without persisting another piece of state.
// Keep the derivation stable: changing it changes upstream conversation affinity.
func PromptCacheKey(chatID string) string {
	if chatID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(chatID))
	return "apc_" + hex.EncodeToString(sum[:24])
}

// ApplyPromptCacheKey runs after provider compatibility overrides. A static
// configured value must not collapse all Chats onto the same upstream route.
func ApplyPromptCacheKey(body map[string]any, chatID string) {
	delete(body, "prompt_cache_key")
	if key := PromptCacheKey(chatID); key != "" {
		body["prompt_cache_key"] = key
	}
}
