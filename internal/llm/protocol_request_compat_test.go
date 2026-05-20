package llm

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	. "agent-platform/internal/models"
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

func TestPreserveReasoningContentRequiresCompatOnly(t *testing.T) {
	protocolConfig := protocolRuntimeConfig{
		Compat: map[string]any{
			"messages": map[string]any{
				"preserveReasoningContent": true,
			},
		},
	}
	if !preserveReasoningContent(protocolConfig, StageSettings{}) {
		t.Fatal("expected compat flag to preserve reasoning_content even when reasoning is disabled")
	}
	if !preserveReasoningContent(protocolConfig, StageSettings{ReasoningEnabled: true}) {
		t.Fatal("expected compat flag plus enabled reasoning to preserve reasoning_content")
	}
	if preserveReasoningContent(protocolRuntimeConfig{}, StageSettings{ReasoningEnabled: true}) {
		t.Fatal("expected missing compat flag to suppress reasoning_content preservation")
	}
}

func TestOpenAIProtocolPrepareRequestReasoningContentCompat(t *testing.T) {
	tests := []struct {
		name             string
		reasoningEnabled bool
		compat           map[string]any
		wantReasoning    bool
	}{
		{name: "default strips reasoning", reasoningEnabled: true, wantReasoning: false},
		{
			name:             "compat preserves reasoning",
			reasoningEnabled: true,
			compat: map[string]any{
				"messages": map[string]any{
					"preserveReasoningContent": true,
				},
			},
			wantReasoning: true,
		},
		{
			name:             "compat preserves when reasoning disabled",
			reasoningEnabled: false,
			compat: map[string]any{
				"messages": map[string]any{
					"preserveReasoningContent": true,
				},
			},
			wantReasoning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
			prepared, err := protocol.PrepareRequest(protocolStreamParams{
				provider: ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
				model:    ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
				protocolConfig: protocolRuntimeConfig{
					EndpointPath: "/v1/chat/completions",
					Compat:       tc.compat,
				},
				stageSettings: StageSettings{ReasoningEnabled: tc.reasoningEnabled},
				messages: []openAIMessage{
					{Role: "system", Content: "system prompt"},
					{
						Role:             "assistant",
						ReasoningContent: "thinking...",
						ToolCalls: []openAIToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: openAIFunctionCall{
								Name:      "datetime",
								Arguments: "{}",
							},
						}},
					},
					{Role: "tool", ToolCallID: "call_1", Name: "datetime", Content: `{"time":"01:35:03"}`},
				},
				toolSpecs: []openAIToolSpec{{
					Type:     "function",
					Function: openAIToolDefinition{Name: "datetime"},
				}},
			})
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}

			rawMessages, err := json.Marshal(prepared.RequestBody["messages"])
			if err != nil {
				t.Fatalf("marshal messages: %v", err)
			}
			hasReasoning := strings.Contains(string(rawMessages), "reasoning_content")
			if hasReasoning != tc.wantReasoning {
				t.Fatalf("expected reasoning_content present=%v, got messages %s", tc.wantReasoning, rawMessages)
			}
		})
	}
}

func TestNewAssistantTurnMessageReasoningContentCompat(t *testing.T) {
	turn := &providerTurnStream{}
	turn.reasoning.WriteString("thinking...")
	toolCalls := []openAIToolCall{{
		ID:   "call_1",
		Type: "function",
		Function: openAIFunctionCall{
			Name:      "datetime",
			Arguments: "{}",
		},
	}}

	defaultStream := &llmRunStream{
		stageSettings: StageSettings{ReasoningEnabled: true},
	}
	if got := defaultStream.newAssistantTurnMessage(turn, "", toolCalls); got.ReasoningContent != "" {
		t.Fatalf("expected default stream to omit reasoning_content, got %q", got.ReasoningContent)
	}

	deepSeekStream := &llmRunStream{
		protocolConfig: protocolRuntimeConfig{
			Compat: map[string]any{
				"messages": map[string]any{
					"preserveReasoningContent": true,
				},
			},
		},
		stageSettings: StageSettings{},
	}
	got := deepSeekStream.newAssistantTurnMessage(turn, "", toolCalls)
	if got.ReasoningContent != "thinking..." {
		t.Fatalf("expected reasoning_content to be preserved, got %#v", got)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected tool call to be preserved, got %#v", got.ToolCalls)
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
