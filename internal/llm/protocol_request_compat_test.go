package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	. "agent-platform-runner-go/internal/models"
)

func TestCompatRequestOverridesMergeAlwaysAndReasoningScopedEntries(t *testing.T) {
	provider := ProviderDefinition{
		Protocols: map[string]ProtocolDefinition{
			"OPENAI": {
				Compat: map[string]any{
					"request": map[string]any{
						"always": map[string]any{
							"providerAlways": true,
							"shared":         "provider-always",
						},
						"whenReasoningEnabled": map[string]any{
							"providerReasoning": true,
							"shared":            "provider-reasoning",
						},
					},
				},
			},
		},
	}
	model := ModelDefinition{
		Protocol: "OPENAI",
		Compat: map[string]any{
			"request": map[string]any{
				"always": map[string]any{
					"modelAlways": true,
					"shared":      "model-always",
				},
				"whenReasoningEnabled": map[string]any{
					"modelReasoning": true,
					"shared":         "model-reasoning",
				},
			},
		},
	}

	protocolConfig := resolveProtocolRuntimeConfig(provider, model)

	got := compatRequestOverrides(protocolConfig, false)
	if got["providerAlways"] != true || got["modelAlways"] != true {
		t.Fatalf("expected always overrides from provider and model, got %#v", got)
	}
	if got["shared"] != "model-always" {
		t.Fatalf("expected model always override to win, got %#v", got)
	}
	if _, exists := got["providerReasoning"]; exists {
		t.Fatalf("expected reasoning-scoped provider override to be absent, got %#v", got)
	}
	if _, exists := got["modelReasoning"]; exists {
		t.Fatalf("expected reasoning-scoped model override to be absent, got %#v", got)
	}

	got = compatRequestOverrides(protocolConfig, true)
	if got["providerReasoning"] != true || got["modelReasoning"] != true {
		t.Fatalf("expected reasoning-scoped overrides when enabled, got %#v", got)
	}
	if got["shared"] != "model-reasoning" {
		t.Fatalf("expected reasoning-scoped model override to win when enabled, got %#v", got)
	}
}

func TestOpenAIProtocolOpenStreamAlwaysRequestOverridesApplyWithoutReasoning(t *testing.T) {
	tests := []struct {
		name             string
		reasoningEnabled bool
		wantScoped       bool
	}{
		{name: "disabled", reasoningEnabled: false, wantScoped: false},
		{name: "enabled", reasoningEnabled: true, wantScoped: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			engine := NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, server.Client())
			protocol := &openAIProtocol{engine: engine}

			stream, err := protocol.OpenStream(context.Background(), protocolStreamParams{
				provider: ProviderDefinition{
					Key:     "mock",
					BaseURL: server.URL,
					APIKey:  "token",
				},
				model: ModelDefinition{
					Protocol: "OPENAI",
					ModelID:  "mock-model",
				},
				protocolConfig: protocolRuntimeConfig{
					EndpointPath: "/v1/chat/completions",
					Compat: map[string]any{
						"request": map[string]any{
							"always": map[string]any{
								"reasoning_split": true,
							},
							"whenReasoningEnabled": map[string]any{
								"reasoning_mode": "detailed",
							},
						},
					},
				},
				stageSettings: StageSettings{ReasoningEnabled: tc.reasoningEnabled},
				messages: []openAIMessage{
					{Role: "user", Content: "hi"},
				},
			})
			if err != nil {
				t.Fatalf("OpenStream returned error: %v", err)
			}
			if stream != nil && stream.body != nil {
				_ = stream.body.Close()
			}

			if captured["reasoning_split"] != true {
				t.Fatalf("expected reasoning_split=true in request body, got %#v", captured)
			}
			_, hasScoped := captured["reasoning_mode"]
			if hasScoped != tc.wantScoped {
				t.Fatalf("expected reasoning_mode present=%v, got body %#v", tc.wantScoped, captured)
			}
		})
	}
}

func TestAnthropicBuildRequestBodyAlwaysRequestOverridesApplyWithoutReasoning(t *testing.T) {
	tests := []struct {
		name             string
		reasoningEnabled bool
		wantScoped       bool
		wantThinking     bool
	}{
		{name: "disabled", reasoningEnabled: false, wantScoped: false, wantThinking: false},
		{name: "enabled", reasoningEnabled: true, wantScoped: true, wantThinking: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol := &anthropicProtocol{engine: &LLMAgentEngine{}}
			body, _, err := protocol.buildRequestBody(
				ModelDefinition{ModelID: "claude-test"},
				StageSettings{ReasoningEnabled: tc.reasoningEnabled},
				[]openAIMessage{{Role: "user", Content: "hi"}},
				nil,
				"",
				protocolRuntimeConfig{
					Compat: map[string]any{
						"request": map[string]any{
							"always": map[string]any{
								"anthropic-beta": "tools-2024-04-04",
							},
							"whenReasoningEnabled": map[string]any{
								"thinking": map[string]any{
									"budget_tokens": 8192,
								},
							},
						},
					},
				},
			)
			if err != nil {
				t.Fatalf("buildRequestBody returned error: %v", err)
			}

			if body["anthropic-beta"] != "tools-2024-04-04" {
				t.Fatalf("expected unconditional anthropic override, got %#v", body)
			}
			_, hasThinking := body["thinking"]
			if hasThinking != tc.wantThinking {
				t.Fatalf("expected thinking present=%v, got %#v", tc.wantThinking, body)
			}
			if tc.wantScoped {
				thinking, _ := body["thinking"].(map[string]any)
				if thinking["budget_tokens"] != 8192 {
					t.Fatalf("expected compat thinking override to win, got %#v", body)
				}
			}
		})
	}
}
