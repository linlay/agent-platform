package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/modelrequest"
	. "agent-platform/internal/models"
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
	modelrequest.ApplyAnthropicMetadata(requestBody, modelrequest.AffinityKey(params.chatID, params.runID))
	body, err := json.Marshal(requestBody)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	normalizedBody, err := normalizePreparedRequestBody(body)
	if err != nil {
		return preparedProviderRequest{}, err
	}
	authHeader, authValue, err := params.provider.AuthHeader("X-Api-Key")
	if err != nil {
		return preparedProviderRequest{}, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		authHeader:     authValue,
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

	return p.engine.executeProviderRequest(req, params.modelTimeout, params.provider.RequestIDHeader)
}

// Anthropic stream events decode into typed fields so provider text reaches
// the turn verbatim; contracts helpers normalize values and must not read it.
type anthropicStreamEvent struct {
	Type    string               `json:"type"`
	Index   anthropicBlockIndex  `json:"index"`
	Message anthropicStreamUsage `json:"message"`
	Block   anthropicStreamBlock `json:"content_block"`
	Delta   anthropicStreamDelta `json:"delta"`
	Usage   map[string]any       `json:"usage"`
	Error   json.RawMessage      `json:"error"`
}

type anthropicStreamUsage struct {
	Usage map[string]any `json:"usage"`
}

type anthropicStreamBlock struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Text     string         `json:"text"`
	Thinking string         `json:"thinking"`
	Input    map[string]any `json:"input"`
}

type anthropicStreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthropicBlockIndex keeps accepting relays that quote the block index.
type anthropicBlockIndex int

func (i *anthropicBlockIndex) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*i = anthropicBlockIndex(AnyIntNode(value))
	return nil
}

func (p *anthropicProtocol) ConsumeChunk(s *llmRunStream, eventName string, rawChunk string) (bool, error) {
	s.currentTurn.trace.markStreamEvent("handled")
	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(rawChunk), &event); err != nil {
		s.currentTurn.observation.DecodeErrors++
		return false, apperrors.Wrap(apperrors.CodeProviderStreamInvalid, fmt.Errorf("decode provider stream chunk: %w", err))
	}

	if err := providerEnvelopeError(event.Error); err != nil {
		return false, s.annotateProviderError(err)
	}
	if eventName == "error" || strings.TrimSpace(event.Type) == "error" {
		return false, s.annotateProviderError(providerReportedError("", "", ""))
	}
	index := int(event.Index)
	switch strings.TrimSpace(eventName) {
	case "", "ping":
		s.currentTurn.trace.markStreamEvent("ignored")
		return false, nil
	case "message_start":
		s.accumulateUsage(mergeAnthropicUsage(s.currentTurn.usage, event.Message.Usage))
	case "content_block_start":
		block := event.Block
		s.currentTurn.observation.RawContentBytes += len(block.Text)
		s.currentTurn.observation.RawReasoningBytes += len(block.Thinking)
		// Anthropic starts text blocks empty, but compatible relays may put the
		// first fragment here; it belongs to the block like any later delta.
		s.appendCompatContent(block.Text)
		s.appendAnthropicThinking(index, block.Thinking)
		if strings.TrimSpace(block.Type) == "tool_use" {
			s.currentTurn.observation.RawToolDeltas++
			var argsChunk string
			if len(block.Input) > 0 {
				data, err := json.Marshal(block.Input)
				if err != nil {
					return false, err
				}
				argsChunk = string(data)
			}
			deltas := s.currentTurn.appendToolCallDelta(index, strings.TrimSpace(block.ID), "function", strings.TrimSpace(block.Name), argsChunk)
			if !s.allowToolUse {
				return false, nil
			}
			s.appendToolCallDeltas(deltas)
		}
	case "content_block_delta":
		delta := event.Delta
		switch strings.TrimSpace(delta.Type) {
		case "text_delta":
			s.currentTurn.observation.RawContentBytes += len(delta.Text)
			s.appendCompatContent(delta.Text)
		case "thinking_delta":
			s.currentTurn.observation.RawReasoningBytes += len(delta.Thinking)
			s.appendAnthropicThinking(index, delta.Thinking)
		case "input_json_delta":
			s.currentTurn.observation.RawToolDeltas++
			if delta.PartialJSON == "" {
				return false, nil
			}
			deltas := s.currentTurn.appendToolCallDelta(index, "", "function", "", delta.PartialJSON)
			if !s.allowToolUse {
				return false, nil
			}
			s.appendToolCallDeltas(deltas)
		case "signature_delta":
			s.currentTurn.trace.markStreamEvent("ignored")
			return false, nil
		default:
			s.currentTurn.trace.markStreamEvent("ignored")
		}
	case "message_delta":
		// Usage is cumulative, and the final delta includes thinking token counts.
		// Collect it before finishing so both successful and truncated turns retain it.
		s.accumulateUsage(mergeAnthropicUsage(s.currentTurn.usage, event.Usage))
		stopReason := strings.TrimSpace(event.Delta.StopReason)
		if stopReason == "" {
			return false, nil
		}
		if stopReason == "tool_use" {
			stopReason = "tool_calls"
		}
		s.currentTurn.finishReason = stopReason
		s.currentTurn.observation.CompletionTrigger = "message_delta_stop_reason"
		s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		return true, s.finishCurrentTurn()
	case "message_stop":
		s.currentTurn.observation.CompletionTrigger = "message_stop"
		if strings.TrimSpace(s.currentTurn.finishReason) == "" {
			s.currentTurn.finishReason = "end_turn"
			s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		}
		return true, s.finishCurrentTurn()
	default:
		s.currentTurn.observation.IgnoredAnthropicEvents++
		s.currentTurn.trace.markStreamEvent("ignored")
	}

	return false, nil
}

// A turn keeps one reasoning buffer, so consecutive thinking blocks (such as
// a reasoning summary followed by a progress update) need a paragraph break;
// fragments within one block are joined verbatim.
func (s *llmRunStream) appendAnthropicThinking(index int, thinking string) {
	if thinking == "" {
		return
	}
	turn := s.currentTurn
	if turn.anthropicThinkingBlock != index {
		thinking = anthropicBlockSeparator(turn.reasoning.String()) + thinking
		turn.anthropicThinkingBlock = index
	}
	s.appendCompatAnthropicThinking(thinking)
}

func anthropicBlockSeparator(previous string) string {
	switch {
	case previous == "", strings.HasSuffix(previous, "\n\n"):
		return ""
	case strings.HasSuffix(previous, "\n"):
		return "\n"
	default:
		return "\n\n"
	}
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
		"max_tokens": resolveAnthropicMaxTokens(model, stageSettings),
	}
	if strings.TrimSpace(systemPrompt) != "" {
		requestBody["system"] = systemPrompt
	}
	if len(toolSpecs) > 0 && effectiveToolChoice != "" {
		requestBody["tools"] = toAnthropicToolSpecs(toolSpecs)
		requestBody["tool_choice"] = anthropicToolChoice(effectiveToolChoice, stageSettings.ReasoningEnabled)
	}

	if compatRequest := compatRequestOverrides(protocolConfig, stageSettings.ReasoningEnabled); len(compatRequest) > 0 {
		requestBody = mergeAnyMaps(requestBody, compatRequest)
	}
	if stageSettings.MaxOutputTokens > 0 {
		requestBody["max_tokens"] = stageSettings.MaxOutputTokens
	}
	if model.MaxOutputTokens > 0 && AnyIntNode(requestBody["max_tokens"]) > model.MaxOutputTokens {
		return nil, "", fmt.Errorf("Anthropic max_tokens exceeds model %s maxOutputTokens (%d)", model.Key, model.MaxOutputTokens)
	}
	delete(requestBody, "thinking")
	outputConfig := CloneMap(AnyMapNode(requestBody["output_config"]))
	delete(outputConfig, "effort")
	if stageSettings.ReasoningEnabled {
		effort, ok := NormalizeReasoningEffort(stageSettings.ReasoningEffort)
		if !ok || effort == ReasoningEffortNone {
			return nil, "", fmt.Errorf("Anthropic reasoning effort must be LOW, MEDIUM, HIGH, XHIGH or MAX")
		}
		if effort == "" {
			effort = ReasoningEffortMedium
		}
		requestBody["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		if outputConfig == nil {
			outputConfig = map[string]any{}
		}
		outputConfig["effort"] = strings.ToLower(effort)
	}
	if len(outputConfig) > 0 {
		requestBody["output_config"] = outputConfig
	} else {
		delete(requestBody, "output_config")
	}
	modelrequest.ApplyAnthropicSampling(requestBody, stageSettings.Sampling)
	applyAnthropicCacheBreakpoints(requestBody)

	return requestBody, effectiveToolChoice, nil
}

func applyAnthropicCacheBreakpoints(body map[string]any) {
	// Own the wire-only markers after compat. Do not combine automatic caching
	// with these three explicit breakpoints or retain markers on older turns.
	delete(body, "cache_control")
	if text, ok := body["system"].(string); ok && strings.TrimSpace(text) != "" {
		body["system"] = []map[string]any{{"type": "text", "text": text}}
	}
	if blocks, ok := anthropicCacheBlockCopies(body["system"]); ok {
		body["system"] = blocks
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i]["type"] == "text" && strings.TrimSpace(AnyStringNode(blocks[i]["text"])) != "" {
				blocks[i]["cache_control"] = map[string]any{"type": "ephemeral"}
				break
			}
		}
	}
	tools, hasTools := anthropicCacheBlockCopies(body["tools"])
	if hasTools {
		body["tools"] = tools
		if len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}
	messages, ok := anthropicCacheBlockCopies(body["messages"])
	if !ok {
		return
	}
	body["messages"] = messages
	for _, message := range messages {
		if blocks, ok := anthropicCacheBlockCopies(message["content"]); ok {
			message["content"] = blocks
		}
	}
	// A one-shot summary has no later turn to reuse a cached message prefix.
	if len(messages) == 0 || (len(tools) == 0 && len(messages) == 1) {
		return
	}
	last := messages[len(messages)-1]
	if last["role"] != "user" {
		return
	}
	if text, ok := last["content"].(string); ok && strings.TrimSpace(text) != "" {
		last["content"] = []map[string]any{{"type": "text", "text": text}}
	}
	blocks, ok := last["content"].([]map[string]any)
	if !ok || len(blocks) == 0 {
		return
	}
	block := blocks[len(blocks)-1]
	switch block["type"] {
	case "text":
		if strings.TrimSpace(AnyStringNode(block["text"])) == "" {
			return
		}
	case "image", "tool_result":
	default:
		return
	}
	block["cache_control"] = map[string]any{"type": "ephemeral"}
}

// Detach protocol-level maps, including compat arrays. Nested schemas, tool
// inputs and tool-result contents remain opaque; their keys are business data.
func anthropicCacheBlockCopies(value any) ([]map[string]any, bool) {
	var blocks []map[string]any
	switch items := value.(type) {
	case []map[string]any:
		blocks = items
	case []any:
		blocks = make([]map[string]any, len(items))
		for i, item := range items {
			blocks[i] = AnyMapNode(item)
		}
	default:
		return nil, false
	}
	out := make([]map[string]any, len(blocks))
	for i, block := range blocks {
		if block == nil {
			return nil, false
		}
		out[i] = CloneMap(block)
		delete(out[i], "cache_control")
	}
	return out, true
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
			blocks := anthropicContentBlocks(msg.Content)
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
					"content":     anthropicToolResultContent(toolMsg.Content),
				})
				index++
			}
			if index < len(messages) && strings.TrimSpace(messages[index].Role) == "user" {
				blocks = append(blocks, anthropicContentBlocks(messages[index].Content)...)
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
	var blocks []map[string]any
	// Assistant text is the model's own output; a replayed turn must not end
	// with whitespace, so only this role keeps the trimmed form.
	for _, block := range anthropicContentBlocks(msg.Content) {
		if block["type"] == "text" {
			text, _ := block["text"].(string)
			block["text"] = strings.TrimSpace(text)
		}
		blocks = append(blocks, block)
	}
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

// User and tool text is sent verbatim: boundary whitespace such as the first
// line's indentation is content. Anthropic rejects whitespace-only text
// blocks, so those are omitted.
func anthropicContentBlocks(content any) []map[string]any {
	parts, ok := anthropicContentParts(content)
	if !ok {
		return anthropicTextBlocks(content)
	}
	blocks := make([]map[string]any, 0, len(parts))
	for _, block := range parts {
		switch AnyStringNode(block["type"]) {
		case "text":
			text, _ := block["text"].(string)
			if strings.TrimSpace(text) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
		case "image_url":
			imageURL := AnyMapNode(block["image_url"])
			mediaType, data, ok := parseDataImageURL(AnyStringNode(imageURL["url"]))
			if ok {
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": mediaType,
						"data":       data,
					},
				})
			}
		}
	}
	return blocks
}

// Content parts are []map[string]any when built in process and []any after a
// JSON round trip; both carry the same OpenAI-style blocks.
func anthropicContentParts(content any) ([]map[string]any, bool) {
	switch value := content.(type) {
	case []map[string]any:
		return value, true
	case []any:
		parts := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if part, ok := item.(map[string]any); ok {
				parts = append(parts, part)
			}
		}
		return parts, true
	default:
		return nil, false
	}
}

func anthropicToolResultContent(content any) any {
	if _, ok := anthropicContentParts(content); ok {
		if blocks := anthropicContentBlocks(content); len(blocks) > 0 {
			return blocks
		}
		return ""
	}
	if text, ok := content.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return anthropicTextFromContent(content)
}

func anthropicTextBlocks(content any) []map[string]any {
	text, ok := content.(string)
	if !ok {
		text = anthropicTextFromContent(content)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []map[string]any{{"type": "text", "text": text}}
}

// anthropicTextFromContent flattens content for the system prompt and for
// non-text values; it never formats Go maps with %v.
func anthropicTextFromContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case nil:
		return ""
	}
	if parts, ok := anthropicContentParts(content); ok {
		var text strings.Builder
		for _, part := range parts {
			if value, ok := part["text"].(string); ok {
				text.WriteString(value)
			}
		}
		return strings.TrimSpace(text.String())
	}
	data, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func parseDataImageURL(url string) (string, string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return "", "", false
	}
	header, data, ok := strings.Cut(strings.TrimPrefix(url, prefix), ",")
	if !ok || strings.TrimSpace(data) == "" {
		return "", "", false
	}
	mediaType, encoding, ok := strings.Cut(header, ";")
	if !ok || strings.ToLower(strings.TrimSpace(encoding)) != "base64" {
		return "", "", false
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if !filetools.IsSupportedImageMime(mediaType) {
		return "", "", false
	}
	return mediaType, data, true
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

func resolveAnthropicMaxTokens(model ModelDefinition, stageSettings StageSettings) int {
	if stageSettings.MaxOutputTokens > 0 {
		return stageSettings.MaxOutputTokens
	}
	if model.MaxOutputTokens > 0 {
		return model.MaxOutputTokens
	}
	return defaultAnthropicMaxOutputTokens
}
