package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
)

type openAIProtocol struct {
	engine *LLMAgentEngine
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatRequest struct {
	Model         string           `json:"model"`
	Messages      []openAIMessage  `json:"messages"`
	Tools         []openAIToolSpec `json:"tools,omitempty"`
	ToolChoice    string           `json:"tool_choice,omitempty"`
	Temperature   float64          `json:"temperature,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIToolSpec struct {
	Type     string               `json:"type"`
	Function openAIToolDefinition `json:"function"`
}

type openAIToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string                  `json:"content"`
			ReasoningContent string                  `json:"reasoning_content"`
			ReasoningDetails []map[string]any        `json:"reasoning_details"`
			ToolCalls        []openAIStreamToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIStreamToolDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id"`
	Type     string                    `json:"type"`
	Function openAIStreamFunctionDelta `json:"function"`
}

type openAIStreamFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (p *openAIProtocol) PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error) {
	endpoint, err := resolveProviderEndpoint(params)
	if err != nil {
		return preparedProviderRequest{}, err
	}

	effectiveToolChoice := "auto"
	if params.toolChoice != "" {
		effectiveToolChoice = params.toolChoice
	}
	if len(params.toolSpecs) == 0 {
		effectiveToolChoice = ""
	}
	requestBody := map[string]any{
		"model":          params.model.ModelID,
		"messages":       params.messages,
		"temperature":    0,
		"stream":         true,
		"stream_options": &streamOptions{IncludeUsage: true},
	}
	if len(params.toolSpecs) > 0 {
		requestBody["tools"] = params.toolSpecs
	}
	if effectiveToolChoice != "" {
		requestBody["tool_choice"] = effectiveToolChoice
	}
	if compatRequest := compatRequestOverrides(params.protocolConfig, params.stageSettings.ReasoningEnabled); len(compatRequest) > 0 {
		requestBody = mergeAnyMaps(requestBody, compatRequest)
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
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Authorization": "Bearer " + params.provider.APIKey,
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

func (p *openAIProtocol) OpenStream(ctx context.Context, params protocolStreamParams, prepared preparedProviderRequest) (*providerTurnStream, error) {
	effectiveToolChoice := "auto"
	if params.toolChoice != "" {
		effectiveToolChoice = params.toolChoice
	}
	if len(params.toolSpecs) == 0 {
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

func (p *openAIProtocol) ConsumeChunk(s *llmRunStream, _ string, rawChunk string) (bool, error) {
	var decoded openAIStreamResponse
	if err := json.Unmarshal([]byte(rawChunk), &decoded); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}
	if len(decoded.Choices) == 0 {
		if decoded.Usage != nil {
			s.accumulateUsage(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens, decoded.Usage.TotalTokens)
			return false, nil
		}
		return false, fmt.Errorf("provider stream returned no choices")
	}

	if decoded.Usage != nil {
		s.accumulateUsage(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens, decoded.Usage.TotalTokens)
	}

	for _, choice := range decoded.Choices {
		s.appendCompatReasoningFromOpenAI(choice.Delta.ReasoningContent, choice.Delta.ReasoningDetails)
		s.appendCompatContent(choice.Delta.Content)
		if len(choice.Delta.ToolCalls) > 0 {
			s.currentTurn.hasMeaningful = true
			for _, toolDelta := range choice.Delta.ToolCalls {
				deltas := s.currentTurn.appendOpenAIToolDelta(toolDelta)
				s.engine.logParsedToolDelta(s.session.RunID, toolDelta)
				if !s.allowToolUse {
					continue
				}
				s.pending = append(s.pending, deltas...)
			}
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			s.currentTurn.finishReason = strings.TrimSpace(choice.FinishReason)
			s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
			if decoded.Usage == nil {
				s.drainUsageChunk()
			}
			return true, s.finishCurrentTurn()
		}
	}

	return false, nil
}

func toOpenAIToolSpecs(defs []api.ToolDetailResponse) []openAIToolSpec {
	out := make([]openAIToolSpec, 0, len(defs))
	for _, def := range defs {
		out = append(out, openAIToolSpec{
			Type: "function",
			Function: openAIToolDefinition{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}
	return out
}

// rawMessageToOpenAI converts a raw_messages.jsonl entry to an openAIMessage.
// Format follows the Java version: role + content, with tool_calls for assistant messages.
func rawMessageToOpenAI(raw map[string]any) openAIMessage {
	role, _ := raw["role"].(string)
	content, _ := raw["content"].(string)
	if role == "" {
		return openAIMessage{}
	}
	msg := openAIMessage{Role: role, Content: content}
	if role == "assistant" {
		if calls, ok := raw["tool_calls"].([]any); ok {
			for _, c := range calls {
				callMap, _ := c.(map[string]any)
				if callMap == nil {
					continue
				}
				tc := openAIToolCall{}
				tc.ID, _ = callMap["id"].(string)
				tc.Type, _ = callMap["type"].(string)
				if tc.Type == "" {
					tc.Type = "function"
				}
				if fn, ok := callMap["function"].(map[string]any); ok {
					tc.Function.Name, _ = fn["name"].(string)
					tc.Function.Arguments, _ = fn["arguments"].(string)
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
			}
			if len(msg.ToolCalls) > 0 && content == "" {
				msg.Content = nil
			}
		}
	}
	if role == "tool" {
		msg.ToolCallID, _ = raw["tool_call_id"].(string)
		msg.Name, _ = raw["name"].(string)
	}
	// Drop empty assistant messages (no meaningful content, no tool_calls)
	if role == "assistant" {
		hasContent := false
		if s, ok := msg.Content.(string); ok && strings.TrimSpace(s) != "" {
			hasContent = true
		}
		if !hasContent && len(msg.ToolCalls) == 0 {
			return openAIMessage{} // Role="" → filtered by caller
		}
	}
	return msg
}

// mergeRawMessagesByMsgID merges multiple raw assistant messages that share the
// same _msgId into a single entry. This is necessary because the storage layer
// writes reasoning, content, and each tool_call as separate StoredMessage
// entries (with a shared _msgId), but the OpenAI protocol requires all
// tool_calls from one turn to be in a single assistant message.
func mergeRawMessagesByMsgID(raw []map[string]any) []map[string]any {
	var result []map[string]any
	msgIDIndex := map[string]int{} // _msgId → index in result

	for _, entry := range raw {
		role, _ := entry["role"].(string)
		msgID, _ := entry["_msgId"].(string)

		// Only merge assistant messages with a _msgId
		if role != "assistant" || msgID == "" {
			result = append(result, entry)
			continue
		}

		idx, seen := msgIDIndex[msgID]
		if !seen {
			// First occurrence: clone the entry and record its position
			merged := map[string]any{}
			for k, v := range entry {
				merged[k] = v
			}
			msgIDIndex[msgID] = len(result)
			result = append(result, merged)
			continue
		}

		// Merge into existing entry at idx
		existing := result[idx]

		// Merge content (string concatenation)
		if newContent, _ := entry["content"].(string); newContent != "" {
			if oldContent, _ := existing["content"].(string); oldContent != "" {
				existing["content"] = oldContent + newContent
			} else {
				existing["content"] = newContent
			}
		}

		// Merge reasoning_content (string concatenation)
		if newRC, _ := entry["reasoning_content"].(string); newRC != "" {
			if oldRC, _ := existing["reasoning_content"].(string); oldRC != "" {
				existing["reasoning_content"] = oldRC + newRC
			} else {
				existing["reasoning_content"] = newRC
			}
		}

		// Merge tool_calls (append to array)
		if newCalls, ok := entry["tool_calls"].([]any); ok && len(newCalls) > 0 {
			if oldCalls, ok := existing["tool_calls"].([]any); ok {
				existing["tool_calls"] = append(oldCalls, newCalls...)
			} else {
				existing["tool_calls"] = newCalls
			}
		}
	}

	// Cleanup: drop assistant messages with no content and no tool_calls
	cleaned := make([]map[string]any, 0, len(result))
	for _, entry := range result {
		role, _ := entry["role"].(string)
		if role == "assistant" {
			content, _ := entry["content"].(string)
			_, hasToolCalls := entry["tool_calls"].([]any)
			if strings.TrimSpace(content) == "" && !hasToolCalls {
				continue // drop empty assistant message
			}
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
