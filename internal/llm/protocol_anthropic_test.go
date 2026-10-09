package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestAnthropicPrepareRequestUsesAdaptiveThinking(t *testing.T) {
	tests := []struct {
		name     string
		settings StageSettings
		effort   string
	}{
		{name: "disabled"},
		{name: "disabled with effort", settings: StageSettings{ReasoningEffort: "HIGH"}},
		{name: "default", settings: StageSettings{ReasoningEnabled: true}, effort: "medium"},
		{name: "low", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "LOW"}, effort: "low"},
		{name: "medium", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "MEDIUM"}, effort: "medium"},
		{name: "high", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "HIGH"}, effort: "high"},
		{name: "xhigh", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "XHIGH"}, effort: "xhigh"},
		{name: "max", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "MAX"}, effort: "max"},
		{name: "normalized", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: " extra_high "}, effort: "xhigh"},
	}
	for _, modelID := range []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"} {
		for _, tc := range tests {
			t.Run(modelID+"/"+tc.name, func(t *testing.T) {
				provider := models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"}
				model := models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: modelID}
				prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
					provider: provider, model: model, stageSettings: tc.settings,
					protocolConfig: resolveProtocolRuntimeConfig(provider, model),
					messages:       []openAIMessage{{Role: "user", Content: "hi"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(prepared.RequestBodyJSON, &body); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(body, prepared.RequestBody) {
					t.Fatalf("wire body and debug payload differ: %#v != %#v", body, prepared.RequestBody)
				}
				if tc.effort == "" {
					if body["thinking"] != nil || body["output_config"] != nil {
						t.Fatalf("unexpected explicit reasoning configuration: %#v", body)
					}
					return
				}
				if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "adaptive", "display": "summarized"}) ||
					!reflect.DeepEqual(body["output_config"], map[string]any{"effort": tc.effort}) {
					t.Fatalf("unexpected reasoning configuration: %#v", body)
				}
			})
		}
	}
}

func TestAnthropicPrepareRequestOwnsThinkingAndEffort(t *testing.T) {
	outputConfig := map[string]any{"effort": "max", "format": map[string]any{"type": "json_schema"}}
	thinking := map[string]any{"type": "custom", "display": "omitted", "custom_option": true}
	for _, enabled := range []bool{true, false} {
		prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
			provider: models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"},
			model:    models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-opus-5-5"},
			stageSettings: StageSettings{
				ReasoningEnabled: enabled, ReasoningEffort: "HIGH",
			},
			messages: []openAIMessage{{Role: "user", Content: "hi"}},
			protocolConfig: protocolRuntimeConfig{Compat: map[string]any{
				"request": map[string]any{"always": map[string]any{
					"thinking": thinking, "output_config": outputConfig, "provider_option": true,
				}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		wantOutput := map[string]any{"format": map[string]any{"type": "json_schema"}}
		if enabled {
			wantOutput["effort"] = "high"
			if !reflect.DeepEqual(prepared.RequestBody["thinking"], map[string]any{"type": "adaptive", "display": "summarized"}) {
				t.Fatalf("compat changed thinking configuration: %#v", prepared.RequestBody)
			}
		} else if prepared.RequestBody["thinking"] != nil {
			t.Fatalf("compat added explicit thinking configuration: %#v", prepared.RequestBody)
		}
		if !reflect.DeepEqual(prepared.RequestBody["output_config"], wantOutput) || prepared.RequestBody["provider_option"] != true {
			t.Fatalf("unexpected compat configuration: %#v", prepared.RequestBody)
		}
		if outputConfig["effort"] != "max" || thinking["type"] != "custom" || thinking["display"] != "omitted" || len(thinking) != 3 {
			t.Fatalf("request preparation mutated shared compat: %#v, %#v", outputConfig, thinking)
		}
	}
}

func TestAnthropicPrepareRequestRejectsInvalidActiveEffort(t *testing.T) {
	for _, effort := range []string{"NONE", "INVALID"} {
		t.Run(effort, func(t *testing.T) {
			_, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
				provider:      models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"},
				model:         models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-opus-5-5"},
				stageSettings: StageSettings{ReasoningEnabled: true, ReasoningEffort: effort},
				messages:      []openAIMessage{{Role: "user", Content: "hi"}},
			})
			if err == nil || !strings.Contains(err.Error(), "Anthropic reasoning effort") {
				t.Fatalf("expected invalid active effort to fail before sending the request, got %v", err)
			}
		})
	}
}

func TestResolveAnthropicMaxTokensUsesStageMaxOutputTokens(t *testing.T) {
	got := resolveAnthropicMaxTokens(models.ModelDefinition{MaxOutputTokens: 128000}, StageSettings{MaxOutputTokens: 8192})
	if got != 8192 {
		t.Fatalf("expected stage max output tokens 8192, got %d", got)
	}
}

func TestResolveAnthropicMaxTokensFallsBackToSourceDefault(t *testing.T) {
	got := resolveAnthropicMaxTokens(models.ModelDefinition{}, StageSettings{})
	if got != 32768 {
		t.Fatalf("expected default max output tokens 32768, got %d", got)
	}
}

func TestAnthropicPrepareRequestOutputLimitPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage int
		model int
		limit any
		want  float64
	}{
		{name: "source default", want: 32768},
		{name: "model configuration", model: 128000, want: 128000},
		{name: "smaller model limit", model: 16000, want: 16000},
		{name: "explicit compat", limit: 65536, want: 65536},
		{name: "compat lowers model limit", model: 128000, limit: 65536, want: 65536},
		{name: "stage beats compat", stage: 16384, limit: 65536, want: 16384},
		{name: "stage lowers model limit", model: 128000, stage: 16384, want: 16384},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compat := map[string]any{}
			if tc.limit != nil {
				compat["max_tokens"] = tc.limit
			}
			prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
				provider:      models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"},
				model:         models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-haiku-5-5", MaxOutputTokens: tc.model},
				stageSettings: StageSettings{MaxOutputTokens: tc.stage},
				protocolConfig: protocolRuntimeConfig{Compat: map[string]any{
					"request": map[string]any{"always": compat},
				}},
				messages: []openAIMessage{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.RequestBody["max_tokens"] != tc.want {
				t.Fatalf("max_tokens = %#v, want %v", prepared.RequestBody["max_tokens"], tc.want)
			}
		})
	}
}

func TestAnthropicPrepareRequestCacheBreakpoints(t *testing.T) {
	system := openAIMessage{Role: "system", Content: "stable system"}
	user := openAIMessage{Role: "user", Content: "first query"}
	assistant := openAIMessage{Role: "assistant", Content: "answer"}
	tools := []openAIToolSpec{
		{Type: "function", Function: openAIToolDefinition{Name: "alpha", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: openAIToolDefinition{Name: "beta", Parameters: map[string]any{"type": "object"}}},
	}
	toolCalls := openAIMessage{Role: "assistant", ToolCalls: []openAIToolCall{
		{ID: "call-a", Type: "function", Function: openAIFunctionCall{Name: "alpha", Arguments: `{"z":1,"a":2}`}},
		{ID: "call-b", Type: "function", Function: openAIFunctionCall{Name: "beta", Arguments: `{}`}},
	}}
	results := []openAIMessage{
		{Role: "tool", ToolCallID: "call-a", Content: "result a"},
		{Role: "tool", ToolCallID: "call-b", Content: "result b"},
	}
	toolHistory := append([]openAIMessage{system, user, toolCalls}, results...)
	for _, tc := range []struct {
		name     string
		messages []openAIMessage
		tools    []openAIToolSpec
		want     []string
	}{
		{name: "first tool-enabled query", messages: []openAIMessage{system, user}, tools: tools, want: []string{"tools[1]", "system[0]", "messages[0].content[0]"}},
		{name: "first tool-free query", messages: []openAIMessage{system, user}, want: []string{"system[0]"}},
		{name: "one-shot summary", messages: []openAIMessage{{Role: "user", Content: "summarize once"}}},
		{name: "tool-free conversation", messages: []openAIMessage{system, user, assistant, {Role: "user", Content: "follow-up"}}, want: []string{"system[0]", "messages[2].content[0]"}},
		{name: "parallel tool results", messages: toolHistory, tools: tools, want: []string{"tools[1]", "system[0]", "messages[2].content[1]"}},
		{name: "tool results followed by steer", messages: append(append([]openAIMessage(nil), toolHistory...), openAIMessage{Role: "user", Content: "steer now"}), tools: tools, want: []string{"tools[1]", "system[0]", "messages[2].content[2]"}},
		{name: "last message is assistant", messages: []openAIMessage{system, user, assistant}, tools: tools, want: []string{"tools[1]", "system[0]"}},
		{name: "empty contents", messages: []openAIMessage{{Role: "system", Content: " "}, {Role: "user", Content: " "}}},
		{name: "system only", messages: []openAIMessage{system}, tools: tools, want: []string{"tools[1]", "system[0]"}},
		{name: "image tail", messages: []openAIMessage{system, {Role: "user", Content: []map[string]any{
			{"type": "text", "text": " "},
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,cG5n"}},
		}}}, tools: tools, want: []string{"tools[1]", "system[0]", "messages[0].content[0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.messages)
			if err != nil {
				t.Fatal(err)
			}
			params := protocolStreamParams{
				provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"},
				model:    models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-test"},
				messages: tc.messages, toolSpecs: tc.tools,
			}
			prepared, err := (&anthropicProtocol{}).PrepareRequest(params)
			if err != nil {
				t.Fatal(err)
			}
			if got := anthropicTestCacheLocations(t, prepared.RequestBody); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cache locations = %v, want %v", got, tc.want)
			}
			after, _ := json.Marshal(tc.messages)
			if !bytes.Equal(before, after) {
				t.Fatal("cache markers mutated source messages")
			}
			var wire map[string]any
			if err := json.Unmarshal(prepared.RequestBodyJSON, &wire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wire, prepared.RequestBody) {
				t.Fatal("trace request does not match wire cache markers")
			}
		})
	}
}

func TestAnthropicCacheRepeatedAndRestoredRequestsKeepStablePrefix(t *testing.T) {
	params := protocolStreamParams{
		provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"},
		model:    models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-test"},
		messages: []openAIMessage{
			{Role: "system", Content: "stable rules"},
			{Role: "user", Content: "query"},
			{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "call-1", Type: "function", Function: openAIFunctionCall{Name: "echo", Arguments: `{"z":1,"a":2}`}}}},
			{Role: "tool", ToolCallID: "call-1", Content: "result"},
		},
		toolSpecs: []openAIToolSpec{{Type: "function", Function: openAIToolDefinition{Name: "echo", Parameters: map[string]any{"type": "object"}}}},
	}
	p := &anthropicProtocol{}
	first, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	// Byte equality is stronger than a byte-prefix assertion for identical input.
	if !bytes.Equal(anthropicTestWithoutCache(t, first.RequestBody), anthropicTestWithoutCache(t, second.RequestBody)) {
		t.Fatal("repeated construction changed the cache prefix")
	}
	encoded, _ := json.Marshal(params.messages)
	if err := json.Unmarshal(encoded, &params.messages); err != nil {
		t.Fatal(err)
	}
	restored, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.RequestBodyJSON, restored.RequestBodyJSON) {
		t.Fatal("restoring unmarked messages changed the generated cache markers or prefix")
	}
	params.messages = append(params.messages, openAIMessage{Role: "assistant", Content: "answer"}, openAIMessage{Role: "user", Content: "next query"})
	grown, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	base := anthropicTestWithoutCacheBody(t, first.RequestBody)
	next := anthropicTestWithoutCacheBody(t, grown.RequestBody)
	for _, key := range []string{"tools", "system"} {
		if !reflect.DeepEqual(base[key], next[key]) {
			t.Fatalf("growing history changed %s prefix", key)
		}
	}
	oldMessages := base["messages"].([]any)
	newMessages := next["messages"].([]any)
	if !reflect.DeepEqual(oldMessages, newMessages[:len(oldMessages)]) {
		t.Fatal("growing history rewrote earlier message blocks")
	}
	anthropicTestCacheLocations(t, grown.RequestBody)
	params.messages[0].Content = "different Chat identity and plan tasks"
	otherChat, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(grown.RequestBody["tools"], otherChat.RequestBody["tools"]) {
		t.Fatal("changing Chat-specific system content changed the marked tools prefix")
	}
	if reflect.DeepEqual(grown.RequestBody["system"], otherChat.RequestBody["system"]) {
		t.Fatal("test did not change the Chat-specific system prefix")
	}
}

func TestAnthropicCacheOwnsCompatMarkersWithoutMutatingBusinessData(t *testing.T) {
	marker := map[string]any{"type": "ephemeral", "ttl": "1h"}
	schema := map[string]any{"type": "object", "properties": map[string]any{"cache_control": map[string]any{"type": "string"}}}
	compat := map[string]any{"request": map[string]any{"always": map[string]any{
		"cache_control": marker,
		"system": []any{
			map[string]any{"type": "text", "text": "stable one", "cache_control": marker},
			map[string]any{"type": "text", "text": "stable two", "cache_control": marker},
		},
		"tools": []any{
			map[string]any{"name": "first", "input_schema": schema, "cache_control": marker},
			map[string]any{"name": "second", "input_schema": schema, "cache_control": marker},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "query", "cache_control": marker}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1", "name": "first", "input": map[string]any{"cache_control": "business input"}, "cache_control": marker}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": []any{map[string]any{"type": "text", "text": "result", "cache_control": "opaque result data"}}, "cache_control": marker}}},
		},
	}}}
	before, _ := json.Marshal(compat)
	params := protocolStreamParams{
		provider:       models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"},
		model:          models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-test"},
		protocolConfig: protocolRuntimeConfig{Compat: compat},
	}
	prepared, err := (&anthropicProtocol{}).PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tools[1]", "system[1]", "messages[2].content[0]"}
	if got := anthropicTestCacheLocations(t, prepared.RequestBody); !reflect.DeepEqual(got, want) {
		t.Fatalf("compat markers survived normalized policy: %v", got)
	}
	after, _ := json.Marshal(compat)
	if !bytes.Equal(before, after) {
		t.Fatal("cache normalization mutated shared compat arrays")
	}
	tools := prepared.RequestBody["tools"].([]any)
	wireSchema := tools[0].(map[string]any)["input_schema"]
	if !reflect.DeepEqual(wireSchema, schema) {
		t.Fatal("cache normalization changed a tool schema's cache_control property")
	}
	messages := prepared.RequestBody["messages"].([]any)
	input := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["input"].(map[string]any)
	resultContent := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if input["cache_control"] != "business input" || resultContent["cache_control"] != "opaque result data" {
		t.Fatal("cache normalization changed opaque tool payloads")
	}
}

func anthropicTestCacheLocations(t *testing.T, body map[string]any) []string {
	t.Helper()
	if _, exists := body["cache_control"]; exists {
		t.Fatal("automatic caching must not consume an extra breakpoint")
	}
	var locations []string
	check := func(block map[string]any, path string) {
		if marker, exists := block["cache_control"]; exists {
			if !reflect.DeepEqual(marker, map[string]any{"type": "ephemeral"}) {
				t.Fatalf("unexpected cache policy at %s: %#v", path, marker)
			}
			locations = append(locations, path)
		}
	}
	for _, key := range []string{"tools", "system"} {
		blocks, _ := body[key].([]any)
		for i, value := range blocks {
			check(value.(map[string]any), fmt.Sprintf("%s[%d]", key, i))
		}
	}
	messages, _ := body["messages"].([]any)
	for i, value := range messages {
		blocks, _ := value.(map[string]any)["content"].([]any)
		for j, block := range blocks {
			check(block.(map[string]any), fmt.Sprintf("messages[%d].content[%d]", i, j))
		}
	}
	if len(locations) > 3 {
		t.Fatalf("more than three explicit cache breakpoints: %v", locations)
	}
	return locations
}

func anthropicTestWithoutCacheBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tools", "system"} {
		blocks, _ := copy[key].([]any)
		for _, block := range blocks {
			delete(block.(map[string]any), "cache_control")
		}
	}
	for _, value := range copy["messages"].([]any) {
		blocks, _ := value.(map[string]any)["content"].([]any)
		for _, block := range blocks {
			delete(block.(map[string]any), "cache_control")
		}
	}
	return copy
}

func anthropicTestWithoutCache(t *testing.T, body map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(anthropicTestWithoutCacheBody(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
