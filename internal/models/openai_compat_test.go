package models

import (
	"strings"
	"testing"
)

func TestParseOpenAIResponseCompatDefaultsAndCanonicalValues(t *testing.T) {
	defaults, err := ParseOpenAIResponseCompat(nil)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if defaults.StreamTermination != OpenAIStreamTerminationFinishReason || defaults.TrailingTimeoutMS != OpenAITrailingTimeoutDefaultMS || defaults.PromptCacheMissDerive != "" {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}

	parsed, err := ParseOpenAIResponseCompat(map[string]any{
		"response": map[string]any{
			"stream": map[string]any{
				"termination":       "stream-end",
				"trailingTimeoutMs": 1750,
			},
			"usage": map[string]any{
				"promptTokensDetails": map[string]any{
					"cacheHitTokens": map[string]any{
						"path": "prompt_tokens_details.cached_tokens",
					},
					"cacheMissTokens": map[string]any{
						"path":   nil,
						"derive": "prompt-minus-cache-hit",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("parse compat: %v", err)
	}
	if parsed.StreamTermination != OpenAIStreamTerminationStreamEnd || parsed.TrailingTimeoutMS != 1750 || parsed.PromptCacheMissDerive != OpenAICacheMissDerivePromptMinusCacheHit {
		t.Fatalf("unexpected parsed compat: %#v", parsed)
	}
}

func TestParseOpenAIResponseCompatAcceptsLegacyCacheMissDeriveAlias(t *testing.T) {
	parsed, err := ParseOpenAIResponseCompat(map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"promptTokensDetails": map[string]any{
					"cacheMissTokens": map[string]any{"derive": "promptTokensMinusCacheHitTokens"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("parse legacy derive alias: %v", err)
	}
	if parsed.PromptCacheMissDerive != OpenAICacheMissDerivePromptMinusCacheHit {
		t.Fatalf("legacy alias was not normalized: %#v", parsed)
	}
}

func TestParseOpenAIResponseCompatRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "termination",
			raw:  map[string]any{"response": map[string]any{"stream": map[string]any{"termination": "qwen"}}},
			want: "termination",
		},
		{
			name: "timeout below minimum",
			raw:  map[string]any{"response": map[string]any{"stream": map[string]any{"trailingTimeoutMs": 0}}},
			want: "between 1 and 10000",
		},
		{
			name: "timeout above maximum",
			raw:  map[string]any{"response": map[string]any{"stream": map[string]any{"trailingTimeoutMs": 10001}}},
			want: "between 1 and 10000",
		},
		{
			name: "non integer timeout",
			raw:  map[string]any{"response": map[string]any{"stream": map[string]any{"trailingTimeoutMs": 1.5}}},
			want: "must be an integer",
		},
		{
			name: "derive",
			raw: map[string]any{"response": map[string]any{"usage": map[string]any{"promptTokensDetails": map[string]any{
				"cacheMissTokens": map[string]any{"derive": "provider-specific"},
			}}}},
			want: "prompt-minus-cache-hit",
		},
		{
			name: "path",
			raw: map[string]any{"response": map[string]any{"usage": map[string]any{"promptTokensDetails": map[string]any{
				"cacheHitTokens": map[string]any{"path": "prompt_tokens_details..cached_tokens"},
			}}}},
			want: "empty segments",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseOpenAIResponseCompat(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestLoadModelRegistryValidatesProviderAndModelOpenAICompat(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		root := t.TempDir()
		writeTestProviderAndModel(t, root, strings.Join([]string{
			"apiKey: plain-text",
			"protocols:",
			"  OPENAI:",
			"    compat:",
			"      response:",
			"        stream:",
			"          termination: invalid",
		}, "\n"))
		if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), "protocols.OPENAI") {
			t.Fatalf("expected provider compat validation error, got %v", err)
		}
	})

	t.Run("model", func(t *testing.T) {
		root := t.TempDir()
		writeTestProviderAndModel(t, root, "apiKey: plain-text",
			"compat:",
			"  response:",
			"    stream:",
			"      trailingTimeoutMs: 10001",
		)
		if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), "trailingTimeoutMs") {
			t.Fatalf("expected model compat validation error, got %v", err)
		}
	})
}
