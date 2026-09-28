package modelresponses

import (
	"strings"
	"testing"
)

func TestPromptCacheKeyStableForArbitraryChatIDs(t *testing.T) {
	// A fixed vector protects persisted conversations from a derivation change.
	if got := PromptCacheKey("abc"); got != "apc_ba7816bf8f01cfea414140de5dae2223b00361a396177a9c" {
		t.Fatalf("derivation changed: %q", got)
	}
	seen := map[string]bool{}
	for _, id := range []string{"abc", "ABC", " abc", "会话/客户:一", strings.Repeat("long-chat-id/", 1000)} {
		key := PromptCacheKey(id)
		if len(key) != 52 || seen[key] || key != PromptCacheKey(id) {
			t.Fatalf("unstable or non-distinct key: %q", key)
		}
		seen[key] = true
	}
	body := map[string]any{"prompt_cache_key": "global-static-key"}
	ApplyPromptCacheKey(body, "abc")
	if body["prompt_cache_key"] != PromptCacheKey("abc") {
		t.Fatal("configured key overrode Chat identity")
	}
	ApplyPromptCacheKey(body, "")
	if _, ok := body["prompt_cache_key"]; ok || PromptCacheKey("") != "" {
		t.Fatal("chatless request retained an affinity key")
	}
}
