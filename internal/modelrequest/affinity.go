package modelrequest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// AffinityKey is the session affinity key sent as Responses prompt_cache_key
// and Anthropic metadata.user_id. It is derived from the Chat identity (or the
// Run when there is no Chat), so retries, later Runs and restored sessions reuse
// it without persisting state. Keep the derivation stable: changing it changes
// upstream routing and prompt cache reuse for existing Chats.
func AffinityKey(chatID string, runID string) string {
	subject := chatID
	if subject == "" {
		subject = strings.TrimSpace(runID)
	}
	if subject == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(subject))
	return "apc_" + hex.EncodeToString(sum[:24])
}

// ApplyPromptCacheKey runs after provider compatibility overrides; the platform
// owns prompt_cache_key on Responses requests.
func ApplyPromptCacheKey(body map[string]any, key string) {
	delete(body, "prompt_cache_key")
	if key != "" {
		body["prompt_cache_key"] = key
	}
}

// ApplyAnthropicMetadata runs after provider compatibility overrides; the
// platform owns metadata on Anthropic Messages requests (user_id is its only
// field), which relays use for sticky upstream routing.
func ApplyAnthropicMetadata(body map[string]any, key string) {
	delete(body, "metadata")
	if key != "" {
		body["metadata"] = map[string]any{"user_id": key}
	}
}
