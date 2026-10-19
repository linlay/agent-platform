package modelusage

import (
	"testing"

	"agent-platform/internal/contracts"
)

func TestFromProviderShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want contracts.ModelUsage
		ok   bool
	}{
		{
			name: "openai images",
			raw: map[string]any{
				"input_tokens": 50.0, "output_tokens": 4160.0, "total_tokens": 4210.0,
				"input_tokens_details": map[string]any{"cached_tokens": 20.0, "text_tokens": 50.0},
			},
			want: contracts.ModelUsage{PromptTokens: 50, CompletionTokens: 4160, TotalTokens: 4210, CacheHitTokens: 20, CacheMissTokens: 30},
			ok:   true,
		},
		{
			name: "chat completions",
			raw: map[string]any{
				"prompt_tokens": 10.0, "completion_tokens": 3.0, "total_tokens": 13.0,
				"prompt_tokens_details":     map[string]any{"cached_tokens": 4.0},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 1.0},
			},
			want: contracts.ModelUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13, ReasoningTokens: 1, CacheHitTokens: 4, CacheMissTokens: 6},
			ok:   true,
		},
		{
			name: "anthropic excludes cache from input_tokens",
			raw:  map[string]any{"input_tokens": 8.0, "output_tokens": 2.0, "cache_read_input_tokens": 30.0, "cache_creation_input_tokens": 5.0},
			want: contracts.ModelUsage{PromptTokens: 43, CompletionTokens: 2, TotalTokens: 45, CacheHitTokens: 30, CacheMissTokens: 13},
			ok:   true,
		},
		{
			name: "total only stays total",
			raw:  map[string]any{"total_tokens": 7.0},
			want: contracts.ModelUsage{TotalTokens: 7},
			ok:   true,
		},
		{name: "missing usage", raw: nil, ok: false},
		{name: "zero usage", raw: map[string]any{"input_tokens": 0.0, "output_tokens": 0.0}, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromProvider("image-model", OperationImageGeneration, tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v (%#v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			tc.want.ModelKey = "image-model"
			tc.want.Operation = OperationImageGeneration
			if got != tc.want {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestAttachSkipsMissingUsage(t *testing.T) {
	result := contracts.ToolExecutionResult{}
	Attach(&result, "image-model", OperationImageGeneration, nil)
	if len(result.ModelUsage) != 0 {
		t.Fatalf("missing usage must not be recorded: %#v", result.ModelUsage)
	}
	Attach(&result, "image-model", OperationImageGeneration, map[string]any{"total_tokens": 3.0})
	if len(result.ModelUsage) != 1 || result.ModelUsage[0].TotalTokens != 3 {
		t.Fatalf("expected reported usage, got %#v", result.ModelUsage)
	}
}
