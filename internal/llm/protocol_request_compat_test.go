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
							"chat_template_kwargs": map[string]any{
								"thinking":         false,
								"provider_only":    "provider-value",
								"return_reasoning": false,
							},
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
					"chat_template_kwargs": map[string]any{
						"thinking": true,
					},
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
	kwargs := AnyMapNode(got["chat_template_kwargs"])
	if kwargs["thinking"] != true || kwargs["provider_only"] != "provider-value" || kwargs["return_reasoning"] != false {
		t.Fatalf("expected model nested override plus provider defaults, got %#v", kwargs)
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

func TestOpenAIProtocolPrepareRequestPreservesToolMessageOrderAndGaps(t *testing.T) {
	protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider:       ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
		model:          ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
		protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/chat/completions"},
		messages: []openAIMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "assistant", ToolCalls: []openAIToolCall{
				{ID: "call_1", Type: "function", Function: openAIFunctionCall{Name: "datetime", Arguments: "{}"}},
				{ID: "call_2", Type: "function", Function: openAIFunctionCall{Name: "file_read", Arguments: `{"file_path":"README.md"}`}},
			}},
			{Role: "user", Content: "intervening context"},
			{Role: "tool", ToolCallID: "call_1", Name: "datetime", Content: "2026-07-19T00:00:00Z"},
		},
		toolSpecs: []openAIToolSpec{
			{Type: "function", Function: openAIToolDefinition{Name: "datetime"}},
			{Type: "function", Function: openAIToolDefinition{Name: "file_read"}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	var request struct {
		Messages []openAIMessage `json:"messages"`
	}
	if err := json.Unmarshal(prepared.RequestBodyJSON, &request); err != nil {
		t.Fatalf("decode prepared request: %v", err)
	}
	if len(request.Messages) != 4 {
		t.Fatalf("expected request preparation to preserve all four input messages, got %#v", request.Messages)
	}
	assistant := request.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("expected assistant tool-call block preserved, got %#v", assistant)
	}
	if request.Messages[2].Role != "user" || request.Messages[2].Content != "intervening context" {
		t.Fatalf("expected intervening user message to remain in place, got %#v", request.Messages[2])
	}
	if request.Messages[3].Role != "tool" || request.Messages[3].ToolCallID != "call_1" || request.Messages[3].Content != "2026-07-19T00:00:00Z" {
		t.Fatalf("expected existing tool result to remain in its original position, got %#v", request.Messages[3])
	}
	for _, message := range request.Messages {
		if message.ToolCallID == "call_2" {
			t.Fatalf("request preparation must not synthesize a missing tool result: %#v", request.Messages)
		}
	}
}

func TestOpenAIProtocolPrepareRequestPreservesOrphanToolMessage(t *testing.T) {
	protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider:       ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
		model:          ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
		protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/chat/completions"},
		messages: []openAIMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "tool", ToolCallID: "orphan", Name: "datetime", Content: "result"},
			{Role: "user", Content: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	var request struct {
		Messages []openAIMessage `json:"messages"`
	}
	if err := json.Unmarshal(prepared.RequestBodyJSON, &request); err != nil {
		t.Fatalf("decode prepared request: %v", err)
	}
	if len(request.Messages) != 3 || request.Messages[1].Role != "tool" || request.Messages[1].ToolCallID != "orphan" {
		t.Fatalf("orphan tool message must be forwarded unchanged, got %#v", request.Messages)
	}
}

func TestOpenAIProtocolPrepareRequestAppliesSamplingAfterCompat(t *testing.T) {
	protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider: ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
		model:    ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
		protocolConfig: protocolRuntimeConfig{
			EndpointPath: "/v1/chat/completions",
			Compat: map[string]any{
				"request": map[string]any{
					"always": map[string]any{
						"temperature":      1.2,
						"top_p":            0.1,
						"presence_penalty": 0.5,
					},
				},
			},
		},
		stageSettings: StageSettings{
			Sampling: SamplingSettings{
				Temperature:      float64PtrForTest(0),
				TopP:             float64PtrForTest(0.95),
				PresencePenalty:  float64PtrForTest(0),
				FrequencyPenalty: float64PtrForTest(0.25),
				Seed:             int64PtrForTest(42),
			},
		},
		messages: []openAIMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	assertRequestNumber(t, prepared.RequestBody, "temperature", 0)
	assertRequestNumber(t, prepared.RequestBody, "top_p", 0.95)
	assertRequestNumber(t, prepared.RequestBody, "presence_penalty", 0)
	assertRequestNumber(t, prepared.RequestBody, "frequency_penalty", 0.25)
	assertRequestNumber(t, prepared.RequestBody, "seed", 42)
}

func TestOpenAIProtocolPrepareRequestKeepsDeterministicDefaultTemperature(t *testing.T) {
	protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider:       ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
		model:          ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
		protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/chat/completions"},
		messages: []openAIMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	assertRequestNumber(t, prepared.RequestBody, "temperature", 0)
}

func TestOpenAIProtocolPrepareRequestOmitsStoredToolBase64(t *testing.T) {
	protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
	encoded := "iVBORw0KGgoAAAANSUhEUgAA"
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider: ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
		model:    ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
		protocolConfig: protocolRuntimeConfig{
			EndpointPath: "/v1/chat/completions",
		},
		messages: []openAIMessage{
			{Role: "system", Content: "system prompt"},
			{
				Role: "assistant",
				ToolCalls: []openAIToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: openAIFunctionCall{
						Name:      "file_read",
						Arguments: `{"file_path":"/private/tmp/page1.png"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Name:       "file_read",
				Content:    `{"contentBase64":"` + encoded + `","filePath":"/private/tmp/page1.png","kind":"image","mimeType":"image/png","sizeBytes":573750}`,
			},
		},
		toolSpecs: []openAIToolSpec{{
			Type:     "function",
			Function: openAIToolDefinition{Name: "file_read"},
		}},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	rawBody := string(prepared.RequestBodyJSON)
	if strings.Contains(rawBody, encoded) {
		t.Fatalf("expected stored tool base64 to be omitted from provider request, got %s", rawBody)
	}
	if !strings.Contains(rawBody, `\"contentBase64Omitted\":true`) {
		t.Fatalf("expected omitted marker in provider request, got %s", rawBody)
	}
	if !strings.Contains(rawBody, `\"contentBase64Chars\":24`) {
		t.Fatalf("expected omitted base64 character count in provider request, got %s", rawBody)
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

func TestOpenAIProtocolPrepareRequestAppliesReasoningScopedChatTemplateKwargs(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reasoningEnabled bool
	}{
		{name: "reasoning disabled", reasoningEnabled: false},
		{name: "reasoning enabled", reasoningEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protocol := &openAIProtocol{engine: NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, &http.Client{})}
			prepared, err := protocol.PrepareRequest(protocolStreamParams{
				provider: ProviderDefinition{Key: "mock", BaseURL: "https://example.com", APIKey: "token"},
				model:    ModelDefinition{Protocol: "OPENAI", ModelID: "mock-model"},
				protocolConfig: protocolRuntimeConfig{
					EndpointPath: "/v1/chat/completions",
					Compat: map[string]any{
						"request": map[string]any{
							"whenReasoningEnabled": map[string]any{
								"chat_template_kwargs": map[string]any{
									"thinking":         true,
									"return_reasoning": true,
								},
							},
						},
					},
				},
				stageSettings: StageSettings{ReasoningEnabled: tc.reasoningEnabled},
				messages: []openAIMessage{
					{Role: "system", Content: "system prompt"},
					{Role: "user", Content: "hi"},
				},
			})
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}

			kwargs, exists := prepared.RequestBody["chat_template_kwargs"]
			if !tc.reasoningEnabled {
				if exists {
					t.Fatalf("expected no chat_template_kwargs when reasoning is disabled, got %#v", kwargs)
				}
				return
			}
			values := AnyMapNode(kwargs)
			if values["thinking"] != true || values["return_reasoning"] != true {
				t.Fatalf("expected reasoning chat template kwargs, got %#v", values)
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

func TestAnthropicPrepareRequestAppliesSupportedSamplingOnly(t *testing.T) {
	protocol := &anthropicProtocol{engine: &LLMAgentEngine{}}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		provider:       ProviderDefinition{Key: "anthropic", BaseURL: "https://example.com", APIKey: "token"},
		model:          ModelDefinition{ModelID: "claude-test"},
		protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/messages"},
		stageSettings: StageSettings{
			Sampling: SamplingSettings{
				Temperature:      float64PtrForTest(0.3),
				TopP:             float64PtrForTest(0.8),
				PresencePenalty:  float64PtrForTest(0.2),
				FrequencyPenalty: float64PtrForTest(0.4),
				Seed:             int64PtrForTest(99),
			},
		},
		messages: []openAIMessage{
			{Role: "system", Content: "anthropic system"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}

	assertRequestNumber(t, prepared.RequestBody, "temperature", 0.3)
	assertRequestNumber(t, prepared.RequestBody, "top_p", 0.8)
	for _, key := range []string{"presence_penalty", "frequency_penalty", "seed"} {
		if _, exists := prepared.RequestBody[key]; exists {
			t.Fatalf("did not expect Anthropic request key %s, got %#v", key, prepared.RequestBody)
		}
	}
}

func float64PtrForTest(value float64) *float64 {
	return &value
}

func int64PtrForTest(value int64) *int64 {
	return &value
}

func assertRequestNumber(t *testing.T, body map[string]any, key string, want float64) {
	t.Helper()
	got, ok := requestNumber(body[key])
	if !ok || got != want {
		t.Fatalf("expected %s=%v, got %#v in body %#v", key, want, body[key], body)
	}
}

func requestNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}
