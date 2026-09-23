package llm

import (
	"agent-platform/internal/apperrors"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/models"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type responsesProtocol struct{ engine *LLMAgentEngine }
type responsesTurnState struct {
	items     map[int]modelresponses.Item
	text      map[int]string
	summaries map[int]string
	args      map[int]string
}

func (p *responsesProtocol) PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error) {
	endpoint, err := resolveProviderEndpoint(params)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	input, err := modelresponses.Input(sanitizeOpenAIToolResultMessages(params.messages), params.model.Key)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	body := map[string]any{"model": params.model.ModelID, "input": input, "store": false, "stream": true, "include": []string{"reasoning.encrypted_content"}}
	if len(params.toolSpecs) > 0 {
		tools, err := modelresponses.Tools(params.toolSpecs)
		if err != nil {
			return preparedProviderRequest{}, err
		}
		body["tools"] = tools
		choice := params.toolChoice
		if choice == "" {
			choice = "auto"
		}
		body["tool_choice"] = choice
	}
	body = mergeAnyMaps(body, compatRequestOverrides(params.protocolConfig, params.stageSettings.ReasoningEnabled))
	if params.stageSettings.ReasoningEnabled {
		effort, ok := models.ResolveReasoningEffort(params.model.ReasoningEffortMapping, params.stageSettings.ReasoningEffort)
		if !ok || effort == "" {
			effort, ok = models.NormalizeReasoningEffort(params.stageSettings.ReasoningEffort)
			if !ok || effort == "" || effort == models.ReasoningEffortNone {
				effort = "medium"
			}
		}
		reasoning := map[string]any{"summary": "auto"}
		if configured, ok := body["reasoning"].(map[string]any); ok {
			reasoning = mergeAnyMaps(reasoning, configured)
		}
		reasoning["effort"] = strings.ToLower(effort)
		body["reasoning"] = reasoning
	}
	if params.stageSettings.MaxOutputTokens > 0 {
		body["max_output_tokens"] = params.stageSettings.MaxOutputTokens
	}
	// Only explicitly configured supported sampling fields are sent. Do not carry
	// Chat Completions' deterministic default, seed or penalties into Responses.
	sampling := params.stageSettings.Sampling
	if sampling.Temperature != nil {
		body["temperature"] = *sampling.Temperature
	}
	if sampling.TopP != nil {
		body["top_p"] = *sampling.TopP
	}
	// Platform history is authoritative even if a provider compat block was copied.
	body["include"] = []string{"reasoning.encrypted_content"}
	body["store"] = false
	body["stream"] = true
	body["input"] = input
	body["model"] = params.model.ModelID
	for _, key := range []string{"previous_response_id", "conversation", "background", "messages", "stream_options", "max_tokens", "max_completion_tokens", "reasoning_effort"} {
		delete(body, key)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	normalized, err := normalizePreparedRequestBody(raw)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	headers := map[string]string{"Content-Type": "application/json", "Accept": "text/event-stream", "Authorization": "Bearer " + params.provider.APIKey}
	for k, v := range params.protocolConfig.Headers {
		headers[k] = v
	}
	return preparedProviderRequest{Endpoint: endpoint, RequestBody: normalized, RequestBodyJSON: raw, Headers: headers}, nil
}
func (p *responsesProtocol) OpenStream(ctx context.Context, params protocolStreamParams, prepared preparedProviderRequest) (*providerTurnStream, error) {
	p.engine.logOutgoingRequest(params.runID, params.provider, params.model, prepared.Endpoint, params.messages, params.toolSpecs, params.toolChoice, prepared.RequestBodyJSON)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.Endpoint, bytes.NewReader(prepared.RequestBodyJSON))
	if err != nil {
		return nil, err
	}
	for k, v := range prepared.Headers {
		req.Header.Set(k, v)
	}
	return p.engine.executeProviderRequest(req, params.modelTimeout)
}
func responsesInvalid(message string) error {
	return apperrors.New(apperrors.CodeProviderStreamInvalid, message)
}
func (p *responsesProtocol) ConsumeChunk(s *llmRunStream, eventName, raw string) (bool, error) {
	var e struct {
		Type        string                  `json:"type"`
		OutputIndex int                     `json:"output_index"`
		Delta       string                  `json:"delta"`
		Item        modelresponses.Item     `json:"item"`
		Response    modelresponses.Response `json:"response"`
		Code        string                  `json:"code"`
		Message     string                  `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		s.currentTurn.observation.DecodeErrors++
		return false, responsesInvalid("decode responses event")
	}
	if e.Type == "" {
		e.Type = eventName
	}
	if e.Type == "" {
		return false, responsesInvalid("responses event missing type")
	}
	t := s.currentTurn
	if t.responses == nil {
		t.responses = &responsesTurnState{items: map[int]modelresponses.Item{}, text: map[int]string{}, summaries: map[int]string{}, args: map[int]string{}}
	}
	state := t.responses
	if e.Response.ID != "" {
		if t.responseID != "" && t.responseID != e.Response.ID {
			return false, responsesInvalid("responses ID changed within stream")
		}
		t.responseID = e.Response.ID
	}
	switch e.Type {
	case "response.output_item.added":
		state.items[e.OutputIndex] = e.Item
	case "response.output_text.delta":
		state.text[e.OutputIndex] += e.Delta
		t.hasMeaningful = true
		t.observation.RawContentBytes += len(e.Delta)
		s.appendCompatContent(e.Delta)
	case "response.reasoning_summary_text.delta":
		state.summaries[e.OutputIndex] += e.Delta
		t.observation.RawReasoningBytes += len(e.Delta)
		s.appendReasoningDelta(e.Delta, "reasoning_content")
	case "response.refusal.delta":
		t.observation.RefusalBytes += len(e.Delta)
	case "response.function_call_arguments.delta":
		state.args[e.OutputIndex] += e.Delta
	case "response.output_item.done":
		state.items[e.OutputIndex] = e.Item
	case "error":
		return false, apperrors.New(apperrors.CodeProviderStreamFailed, fmt.Sprintf("responses error %s: %s", e.Code, e.Message))
	case "response.failed":
		message := "responses request failed"
		if e.Response.Error != nil {
			message = fmt.Sprintf("responses failed %s: %s", e.Response.Error.Code, e.Response.Error.Message)
		}
		return false, apperrors.New(apperrors.CodeProviderStreamFailed, message)
	case "response.completed", "response.incomplete":
		if e.Type == "response.completed" && e.Response.Status != "completed" {
			return false, responsesInvalid("responses completed event has invalid status")
		}
		if e.Type == "response.incomplete" && e.Response.Status != "incomplete" {
			return false, responsesInvalid("responses incomplete event has invalid status")
		}
		if e.Response.Error != nil {
			return false, apperrors.New(apperrors.CodeProviderStreamFailed, e.Response.Error.Message)
		}
		if err := validateResponsesFinal(e.Response, state); err != nil {
			return false, err
		}
		if e.Response.Usage != nil {
			u := e.Response.Usage
			s.accumulateUsage(&openAIUsage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens, PromptCacheHitTokens: u.InputDetails.CachedTokens, PromptCacheMissTokens: max(0, u.InputTokens-u.InputDetails.CachedTokens), PromptTokensDetails: openAIPromptTokenDetails{CachedTokens: u.InputDetails.CachedTokens}, CompletionTokensDetails: openAICompletionTokenDetails{ReasoningTokens: u.OutputDetails.ReasoningTokens}})
		}
		t.finishReason = "stop"
		if e.Type == "response.incomplete" {
			switch e.Response.IncompleteDetails.Reason {
			case "max_output_tokens":
				t.finishReason = "length"
			case "content_filter":
				t.finishReason = "content_filter"
			default:
				return false, apperrors.New(apperrors.CodeProviderStreamFailed, "responses incomplete: "+e.Response.IncompleteDetails.Reason)
			}
		}
		for index, item := range e.Response.Output {
			switch item.Type {
			case "message":
				var text strings.Builder
				for _, c := range item.Content {
					if c.Type == "output_text" {
						text.WriteString(c.Text)
					}
					if c.Type == "refusal" {
						t.observation.RefusalBytes += len(c.Refusal)
					}
				}
				full := text.String()
				seen := state.text[index]
				if !strings.HasPrefix(full, seen) {
					return false, responsesInvalid("responses final text disagrees with deltas")
				}
				if suffix := strings.TrimPrefix(full, seen); suffix != "" {
					s.appendCompatContent(suffix)
					t.observation.RawContentBytes += len(suffix)
				}
			case "reasoning":
				if item.EncryptedContent != "" {
					t.encryptedReasoning = append(t.encryptedReasoning, item.Reasoning())
				}
				var parts []struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(item.Summary, &parts)
				var b strings.Builder
				for _, part := range parts {
					b.WriteString(part.Text)
				}
				summary := b.String()
				seen := state.summaries[index]
				if !strings.HasPrefix(summary, seen) {
					return false, responsesInvalid("responses final summary disagrees with deltas")
				}
				if strings.HasPrefix(summary, seen) {
					if suffix := strings.TrimPrefix(summary, seen); suffix != "" {
						s.appendReasoningDelta(suffix, "reasoning_content")
						t.observation.RawReasoningBytes += len(suffix)
					}
				}
			case "function_call":
				if e.Type != "response.completed" {
					continue
				}
				if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
					return false, responsesInvalid("responses invalid function call")
				}
				if seen := state.args[index]; seen != "" && !strings.HasPrefix(item.Arguments, seen) {
					return false, responsesInvalid("responses final arguments disagree with deltas")
				}
				delta := openAIStreamToolDelta{Index: index, ID: item.CallID, Type: "function", Function: openAIStreamFunctionDelta{Name: item.Name, Arguments: item.Arguments}}
				deltas := t.appendOpenAIToolDelta(delta)
				if s.allowToolUse {
					s.appendToolCallDeltas(deltas)
				}
				t.hasMeaningful = true
				t.finishReason = "tool_calls"
			default:
				return false, responsesInvalid("unsupported responses output item: " + item.Type)
			}
		}
		if t.outputGuardErr != nil {
			return false, t.outputGuardErr
		}
		t.observation.CompletionTrigger = e.Type
		return true, s.finishCurrentTurn()
	}
	if t.outputGuardErr != nil {
		return false, t.outputGuardErr
	}
	return false, nil
}

// Validate the complete snapshot before publishing any tool calls. Deltas alone
// cannot authorize execution, and an omitted item must never silently commit.
func validateResponsesFinal(r modelresponses.Response, state *responsesTurnState) error {
	if r.ID == "" {
		return responsesInvalid("responses terminal event missing ID")
	}
	for index, observed := range state.items {
		if index < 0 || index >= len(r.Output) {
			return responsesInvalid("responses terminal snapshot omitted output item")
		}
		final := r.Output[index]
		if observed.Type != final.Type || (observed.ID != "" && observed.ID != final.ID) {
			return responsesInvalid("responses terminal item identity changed")
		}
	}
	for _, entries := range []map[int]string{state.text, state.summaries, state.args} {
		for index := range entries {
			if index < 0 || index >= len(r.Output) {
				return responsesInvalid("responses terminal snapshot omitted deltas")
			}
		}
	}
	calls := map[string]bool{}
	for index, item := range r.Output {
		if state.text[index] != "" && item.Type != "message" || state.summaries[index] != "" && item.Type != "reasoning" || state.args[index] != "" && item.Type != "function_call" {
			return responsesInvalid("responses delta item type mismatch")
		}
		switch item.Type {
		case "reasoning":
			if item.EncryptedContent != "" && item.ID == "" {
				return responsesInvalid("responses encrypted reasoning missing ID")
			}
			if len(item.Summary) > 0 && string(item.Summary) != "null" {
				var parts []struct {
					Type string  `json:"type"`
					Text *string `json:"text"`
				}
				if err := json.Unmarshal(item.Summary, &parts); err != nil {
					return responsesInvalid("responses invalid reasoning summary")
				}
				for _, part := range parts {
					if part.Type != "summary_text" || part.Text == nil {
						return responsesInvalid("responses invalid reasoning summary part")
					}
				}
			}
		case "function_call":
			if r.Status == "completed" {
				var args map[string]json.RawMessage
				if item.CallID == "" || item.Name == "" || calls[item.CallID] || json.Unmarshal([]byte(item.Arguments), &args) != nil || args == nil {
					return responsesInvalid("responses invalid or duplicate function call")
				}
				calls[item.CallID] = true
			}
		case "message":
			for _, part := range item.Content {
				if part.Type != "output_text" && part.Type != "refusal" {
					return responsesInvalid("unsupported responses message content: " + part.Type)
				}
			}
		default:
			return responsesInvalid("unsupported responses output item: " + item.Type)
		}
	}
	return nil
}
