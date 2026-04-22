package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	. "agent-platform-runner-go/internal/models"
)

type anthropicProtocol struct {
	engine *LLMAgentEngine
}

func (p *anthropicProtocol) PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error) {
	endpoint, err := resolveProviderEndpoint(params)
	if err != nil {
		return preparedProviderRequest{}, err
	}

	requestBody, _, err := p.buildRequestBody(params.model, params.stageSettings, params.messages, params.toolSpecs, params.toolChoice, params.protocolConfig)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	normalizedBody, err := normalizePreparedRequestBody(body)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		"X-Api-Key":    params.provider.APIKey,
	}
	for key, value := range params.protocolConfig.Headers {
		headers[key] = value
	}
	return preparedProviderRequest{
		Endpoint:        endpoint,
		RequestBody:     normalizedBody,
		RequestBodyJSON: body,
		Headers:         headers,
	}, nil
}

func (p *anthropicProtocol) OpenStream(ctx context.Context, params protocolStreamParams, prepared preparedProviderRequest) (*providerTurnStream, error) {
	effectiveToolChoice := strings.TrimSpace(strings.ToLower(params.toolChoice))
	if effectiveToolChoice == "" {
		effectiveToolChoice = "auto"
	}
	if len(params.toolSpecs) == 0 || effectiveToolChoice == "none" {
		effectiveToolChoice = ""
	}
	p.engine.logOutgoingRequest(params.runID, params.provider, params.model, prepared.Endpoint, params.messages, params.toolSpecs, effectiveToolChoice, prepared.RequestBodyJSON)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.Endpoint, bytes.NewReader(prepared.RequestBodyJSON))
	if err != nil {
		return nil, err
	}
	for key, value := range prepared.Headers {
		req.Header.Set(key, value)
	}

	return p.engine.executeProviderRequest(req)
}

func (p *anthropicProtocol) ConsumeChunk(s *llmRunStream, eventName string, rawChunk string) (bool, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawChunk), &payload); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}

	switch strings.TrimSpace(eventName) {
	case "", "ping":
		return false, nil
	case "content_block_start":
		block := AnyMapNode(payload["content_block"])
		blockType := AnyStringNode(block["type"])
		if blockType == "tool_use" {
			index := AnyIntNode(payload["index"])
			input, _ := block["input"].(map[string]any)
			toolID := AnyStringNode(block["id"])
			toolName := AnyStringNode(block["name"])
			var argsChunk string
			if len(input) > 0 {
				data, err := json.Marshal(input)
				if err != nil {
					return false, err
				}
				argsChunk = string(data)
			}
			deltas := s.currentTurn.appendToolCallDelta(index, toolID, "function", toolName, argsChunk)
			if !s.allowToolUse {
				return false, nil
			}
			s.pending = append(s.pending, deltas...)
		}
	case "content_block_delta":
		index := AnyIntNode(payload["index"])
		delta := AnyMapNode(payload["delta"])
		switch AnyStringNode(delta["type"]) {
		case "text_delta":
			s.appendCompatContent(AnyStringNode(delta["text"]))
		case "thinking_delta":
			s.appendCompatAnthropicThinking(AnyStringNode(delta["thinking"]))
		case "input_json_delta":
			partialJSON := AnyStringNode(delta["partial_json"])
			if partialJSON == "" {
				return false, nil
			}
			deltas := s.currentTurn.appendToolCallDelta(index, "", "function", "", partialJSON)
			if !s.allowToolUse {
				return false, nil
			}
			s.pending = append(s.pending, deltas...)
		case "signature_delta":
			return false, nil
		}
	case "message_delta":
		delta := AnyMapNode(payload["delta"])
		stopReason := strings.TrimSpace(AnyStringNode(delta["stop_reason"]))
		if stopReason == "" {
			return false, nil
		}
		if stopReason == "tool_use" {
			stopReason = "tool_calls"
		}
		s.currentTurn.finishReason = stopReason
		s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		return true, s.finishCurrentTurn()
	case "message_stop":
		if strings.TrimSpace(s.currentTurn.finishReason) == "" {
			s.currentTurn.finishReason = "end_turn"
			s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		}
		return true, s.finishCurrentTurn()
	}

	return false, nil
}

func (p *anthropicProtocol) buildRequestBody(model ModelDefinition, stageSettings StageSettings, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, protocolConfig protocolRuntimeConfig) (map[string]any, string, error) {
	systemPrompt, anthropicMessages, err := convertMessagesToAnthropic(messages)
	if err != nil {
		return nil, "", err
	}

	effectiveToolChoice := strings.TrimSpace(strings.ToLower(toolChoice))
	if effectiveToolChoice == "" {
		effectiveToolChoice = "auto"
	}
	if len(toolSpecs) == 0 || effectiveToolChoice == "none" {
		effectiveToolChoice = ""
	}

	requestBody := map[string]any{
		"model":      model.ModelID,
		"messages":   anthropicMessages,
		"stream":     true,
		"max_tokens": resolveAnthropicMaxTokens(p.engine.cfg, stageSettings),
	}
	if strings.TrimSpace(systemPrompt) != "" {
		requestBody["system"] = systemPrompt
	}
	if len(toolSpecs) > 0 && effectiveToolChoice != "" {
		requestBody["tools"] = toAnthropicToolSpecs(toolSpecs)
		requestBody["tool_choice"] = anthropicToolChoice(effectiveToolChoice, stageSettings.ReasoningEnabled)
	}

	if stageSettings.ReasoningEnabled {
		requestBody["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": reasoningBudgetTokens(stageSettings.ReasoningEffort),
		}
	}
	if compatRequest := compatRequestOverrides(protocolConfig, stageSettings.ReasoningEnabled); len(compatRequest) > 0 {
		requestBody = mergeAnyMaps(requestBody, compatRequest)
	}

	return requestBody, effectiveToolChoice, nil
}

func convertMessagesToAnthropic(messages []openAIMessage) (string, []map[string]any, error) {
	var systemParts []string
	var out []map[string]any
	for index := 0; index < len(messages); index++ {
		msg := messages[index]
		switch strings.TrimSpace(msg.Role) {
		case "system":
			if text := anthropicTextFromContent(msg.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			blocks := anthropicTextBlocks(msg.Content)
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		case "assistant":
			blocks, err := anthropicAssistantBlocks(msg)
			if err != nil {
				return "", nil, err
			}
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": blocks})
			}
		case "tool":
			blocks := make([]map[string]any, 0, 2)
			for index < len(messages) && strings.TrimSpace(messages[index].Role) == "tool" {
				toolMsg := messages[index]
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": toolMsg.ToolCallID,
					"content":     anthropicTextFromContent(toolMsg.Content),
				})
				index++
			}
			if index < len(messages) && strings.TrimSpace(messages[index].Role) == "user" {
				blocks = append(blocks, anthropicTextBlocks(messages[index].Content)...)
			} else {
				index--
			}
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		}
	}
	return strings.Join(systemParts, "\n\n"), out, nil
}

func anthropicAssistantBlocks(msg openAIMessage) ([]map[string]any, error) {
	blocks := anthropicTextBlocks(msg.Content)
	for _, toolCall := range msg.ToolCalls {
		input := map[string]any{}
		if strings.TrimSpace(toolCall.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
				return nil, fmt.Errorf("decode assistant tool call %s: %w", toolCall.ID, err)
			}
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    toolCall.ID,
			"name":  toolCall.Function.Name,
			"input": input,
		})
	}
	return blocks, nil
}

func anthropicTextBlocks(content any) []map[string]any {
	text := anthropicTextFromContent(content)
	if text == "" {
		return nil
	}
	return []map[string]any{{"type": "text", "text": text}}
}

func anthropicTextFromContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func toAnthropicToolSpecs(specs []openAIToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"name":         spec.Function.Name,
			"description":  spec.Function.Description,
			"input_schema": spec.Function.Parameters,
		})
	}
	return out
}

func anthropicToolChoice(toolChoice string, reasoningEnabled bool) map[string]any {
	switch strings.TrimSpace(strings.ToLower(toolChoice)) {
	case "required":
		if reasoningEnabled {
			return map[string]any{"type": "auto"}
		}
		return map[string]any{"type": "any"}
	case "auto":
		return map[string]any{"type": "auto"}
	default:
		return map[string]any{"type": "auto"}
	}
}

func resolveAnthropicMaxTokens(cfg config.Config, stageSettings StageSettings) int {
	if stageSettings.MaxTokens > 0 {
		return stageSettings.MaxTokens
	}
	if cfg.Defaults.MaxTokens > 0 {
		return cfg.Defaults.MaxTokens
	}
	return 4096
}

func reasoningBudgetTokens(effort string) int {
	switch strings.ToUpper(strings.TrimSpace(effort)) {
	case "LOW":
		return 1024
	case "HIGH":
		return 4096
	case "MEDIUM":
		fallthrough
	default:
		return 2048
	}
}
