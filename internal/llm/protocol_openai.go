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

func (p *openAIProtocol) OpenStream(ctx context.Context, params protocolStreamParams) (*providerTurnStream, error) {
	endpoint, err := resolveProviderEndpoint(params)
	if err != nil {
		return nil, err
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
	if params.stageSettings.ReasoningEnabled {
		if compatRequest := anyMapNode(anyMapNode(params.protocolConfig.Compat["request"])["whenReasoningEnabled"]); compatRequest != nil {
			requestBody = mergeAnyMaps(requestBody, compatRequest)
		}
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	p.engine.logOutgoingRequest(params.runID, params.provider, params.model, endpoint, params.messages, params.toolSpecs, effectiveToolChoice, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+params.provider.APIKey)
	for key, value := range params.protocolConfig.Headers {
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
		return false, fmt.Errorf("provider stream returned no choices")
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
	return msg
}
