package llm

import (
	"net/http"
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

func TestOpenAIProtocolPrepareRequestExposesDebugPayload(t *testing.T) {
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
			engine := NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})
			protocol := &openAIProtocol{engine: engine}

			prepared, err := protocol.PrepareRequest(protocolStreamParams{
				provider: ProviderDefinition{
					Key:     "mock",
					BaseURL: "https://example.com",
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
					{Role: "system", Content: "system prompt"},
					{Role: "user", Content: "hi"},
				},
				toolSpecs: []openAIToolSpec{{
					Type: "function",
					Function: openAIToolDefinition{
						Name:        "search",
						Description: "search docs",
						Parameters: map[string]any{
							"type": "object",
						},
					},
				}},
			})
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}

			if prepared.Endpoint != "https://example.com/v1/chat/completions" {
				t.Fatalf("unexpected endpoint %q", prepared.Endpoint)
			}
			if prepared.RequestBody["reasoning_split"] != true {
				t.Fatalf("expected reasoning_split=true in request body, got %#v", prepared.RequestBody)
			}
			_, hasScoped := prepared.RequestBody["reasoning_mode"]
			if hasScoped != tc.wantScoped {
				t.Fatalf("expected reasoning_mode present=%v, got body %#v", tc.wantScoped, prepared.RequestBody)
			}
			if _, ok := prepared.RequestBody["messages"].([]any); !ok {
				t.Fatalf("expected normalized messages array, got %#v", prepared.RequestBody["messages"])
			}
			if tools, _ := prepared.RequestBody["tools"].([]any); len(tools) != 1 {
				t.Fatalf("expected one tool in request body, got %#v", prepared.RequestBody)
			}
		})
	}
}

func TestAnthropicPrepareRequestExposesDebugPayload(t *testing.T) {
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
			prepared, err := protocol.PrepareRequest(protocolStreamParams{
				provider: ProviderDefinition{
					Key:     "anthropic",
					BaseURL: "https://example.com",
					APIKey:  "token",
				},
				model:         ModelDefinition{ModelID: "claude-test"},
				stageSettings: StageSettings{ReasoningEnabled: tc.reasoningEnabled},
				messages: []openAIMessage{
					{Role: "system", Content: "anthropic system"},
					{Role: "user", Content: "hi"},
				},
				toolSpecs: []openAIToolSpec{{
					Type: "function",
					Function: openAIToolDefinition{
						Name: "search",
						Parameters: map[string]any{
							"type": "object",
						},
					},
				}},
				protocolConfig: protocolRuntimeConfig{
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
			})
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}

			if prepared.RequestBody["anthropic-beta"] != "tools-2024-04-04" {
				t.Fatalf("expected unconditional anthropic override, got %#v", prepared.RequestBody)
			}
			_, hasThinking := prepared.RequestBody["thinking"]
			if hasThinking != tc.wantThinking {
				t.Fatalf("expected thinking present=%v, got %#v", tc.wantThinking, prepared.RequestBody)
			}
			if tc.wantScoped {
				thinking, _ := prepared.RequestBody["thinking"].(map[string]any)
				if AnyIntNode(thinking["budget_tokens"]) != 8192 {
					t.Fatalf("expected compat thinking override to win, got %#v", prepared.RequestBody)
				}
			}
			if tools, _ := prepared.RequestBody["tools"].([]any); len(tools) != 1 {
				t.Fatalf("expected one tool in request body, got %#v", prepared.RequestBody)
			}
		})
	}
}
