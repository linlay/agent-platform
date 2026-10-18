package modelrequest

import (
	"strings"
	"testing"
)

func TestAffinityKeyStableForArbitraryChatIDs(t *testing.T) {
	// A fixed vector protects existing conversations from a derivation change.
	if got := AffinityKey("abc", "run-1"); got != "apc_ba7816bf8f01cfea414140de5dae2223b00361a396177a9c" {
		t.Fatalf("derivation changed: %q", got)
	}
	seen := map[string]bool{}
	for _, id := range []string{"abc", "ABC", " abc", "会话/客户:一", strings.Repeat("long-chat-id/", 1000)} {
		key := AffinityKey(id, "run")
		if len(key) != 52 || seen[key] || key != AffinityKey(id, "other-run") {
			t.Fatalf("unstable or non-distinct key: %q", key)
		}
		seen[key] = true
	}
}

func TestAffinityKeyFallsBackToRun(t *testing.T) {
	if AffinityKey("", "run-1") != AffinityKey("run-1", "") || AffinityKey("", "run-1") == AffinityKey("", "run-2") {
		t.Fatal("chatless calls must use a per-Run key")
	}
	if AffinityKey("", " ") != "" {
		t.Fatal("no identity must yield no key")
	}
}

func TestApplyAffinityFieldsOverrideConfiguredValues(t *testing.T) {
	key := AffinityKey("abc", "")
	body := map[string]any{"prompt_cache_key": "global-static-key", "metadata": map[string]any{"user_id": "static", "extra": 1}}
	ApplyPromptCacheKey(body, key)
	ApplyAnthropicMetadata(body, key)
	if body["prompt_cache_key"] != key {
		t.Fatal("configured prompt_cache_key overrode Chat identity")
	}
	metadata, _ := body["metadata"].(map[string]any)
	if len(metadata) != 1 || metadata["user_id"] != key {
		t.Fatalf("metadata = %#v", body["metadata"])
	}
	ApplyPromptCacheKey(body, "")
	ApplyAnthropicMetadata(body, "")
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatal("empty key retained prompt_cache_key")
	}
	if _, ok := body["metadata"]; ok {
		t.Fatal("empty key retained metadata")
	}
}
