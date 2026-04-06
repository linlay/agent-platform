package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

type LLMAgentEngine struct {
	cfg        config.Config
	models     *ModelRegistry
	tools      ToolExecutor
	sandbox    SandboxClient
	httpClient *http.Client
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
	Model       string           `json:"model"`
	Messages    []openAIMessage  `json:"messages"`
	Tools       []openAIToolSpec `json:"tools,omitempty"`
	ToolChoice  string           `json:"tool_choice,omitempty"`
	Temperature float64          `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
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
			Content   string                  `json:"content"`
			ToolCalls []openAIStreamToolDelta `json:"tool_calls"`
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

type llmRunStream struct {
	engine    *LLMAgentEngine
	ctx       context.Context
	req       api.QueryRequest
	session   QuerySession
	model     ModelDefinition
	provider  ProviderDefinition
	toolSpecs []openAIToolSpec
	messages  []openAIMessage
	execCtx   *ExecutionContext
	maxSteps  int

	step         int
	deltaIndex   int
	pending      []map[string]any
	currentTurn  *providerTurnStream
	finished     bool
	closed       bool
	fallbackSent bool
}

type providerTurnStream struct {
	body          io.ReadCloser
	reader        *bufio.Reader
	content       strings.Builder
	toolCalls     map[int]*toolCallAccumulator
	finishReason  string
	hasMeaningful bool
}

type toolCallAccumulator struct {
	ID           string
	Type         string
	FunctionName string
	Arguments    strings.Builder
}

func NewLLMAgentEngine(cfg config.Config, models *ModelRegistry, tools ToolExecutor, sandbox SandboxClient) *LLMAgentEngine {
	return &LLMAgentEngine{
		cfg:        cfg,
		models:     models,
		tools:      tools,
		sandbox:    sandbox,
		httpClient: &http.Client{},
	}
}

func (e *LLMAgentEngine) Stream(ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	model, provider, err := e.models.Get(session.ModelKey)
	if err != nil {
		return nil, err
	}
	toolSpecs := toOpenAIToolSpecs(filterToolDefinitions(e.tools.Definitions(), session.ToolNames))
	execCtx := &ExecutionContext{Request: req, Session: session}

	stream := &llmRunStream{
		engine:    e,
		ctx:       ctx,
		req:       req,
		session:   session,
		model:     model,
		provider:  provider,
		toolSpecs: toolSpecs,
		messages: []openAIMessage{
			{
				Role:    "system",
				Content: buildSystemPrompt(session, req, model.Key),
			},
			{
				Role:    "user",
				Content: req.Message,
			},
		},
		execCtx:  execCtx,
		maxSteps: e.resolveMaxSteps(),
	}
	if err := stream.prepareNextTurn(); err != nil {
		stream.Close()
		return nil, err
	}
	if err := stream.prime(); err != nil && !errors.Is(err, io.EOF) {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

func (e *LLMAgentEngine) resolveMaxSteps() int {
	maxSteps := e.cfg.Defaults.React.MaxSteps
	if maxSteps <= 0 {
		return 6
	}
	return maxSteps
}

func (s *llmRunStream) Next() (map[string]any, error) {
	if len(s.pending) == 0 {
		if err := s.fillPending(); err != nil {
			return nil, err
		}
	}
	if len(s.pending) == 0 {
		return nil, io.EOF
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

func (s *llmRunStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.currentTurn != nil && s.currentTurn.body != nil {
		_ = s.currentTurn.body.Close()
		s.currentTurn = nil
	}
	s.engine.sandbox.CloseQuietly(s.execCtx)
	return nil
}

func (s *llmRunStream) prime() error {
	if len(s.pending) > 0 || s.finished {
		return nil
	}
	return s.fillPending()
}

func (s *llmRunStream) fillPending() error {
	for len(s.pending) == 0 {
		if s.finished {
			return io.EOF
		}
		if s.currentTurn == nil {
			if s.step >= s.maxSteps {
				s.enqueueFallback("Tool execution loop reached the maximum number of steps.")
				s.finished = true
				continue
			}
			if err := s.prepareNextTurn(); err != nil {
				return err
			}
		}
		done, err := s.consumeCurrentTurn()
		if err != nil {
			return err
		}
		if done {
			continue
		}
	}
	return nil
}

func (s *llmRunStream) prepareNextTurn() error {
	turn, err := s.engine.openProviderStream(s.ctx, s.provider, s.model, s.messages, s.toolSpecs)
	if err != nil {
		return err
	}
	s.currentTurn = turn
	s.step++
	return nil
}

func (s *llmRunStream) consumeCurrentTurn() (bool, error) {
	rawChunk, err := readSSEData(s.currentTurn.reader)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if s.currentTurn.finishReason == "" && !s.currentTurn.hasMeaningful {
				return false, fmt.Errorf("provider stream ended before first valid event")
			}
			if s.currentTurn.finishReason == "" {
				return false, io.ErrUnexpectedEOF
			}
			return true, s.finishCurrentTurn()
		}
		return false, err
	}

	s.engine.logRawChunk(s.session.RunID, rawChunk)
	if rawChunk == "" {
		return false, nil
	}
	if rawChunk == "[DONE]" {
		return true, s.finishCurrentTurn()
	}

	var decoded openAIStreamResponse
	if err := json.Unmarshal([]byte(rawChunk), &decoded); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return false, fmt.Errorf("provider stream returned no choices")
	}

	for _, choice := range decoded.Choices {
		if choice.Delta.Content != "" {
			s.currentTurn.hasMeaningful = true
			s.currentTurn.content.WriteString(choice.Delta.Content)
			s.engine.logParsedDelta(s.session.RunID, "content", choice.Delta.Content)
			s.pending = append(s.pending, s.newContentDeltaEvent(choice.Delta.Content))
		}
		if len(choice.Delta.ToolCalls) > 0 {
			s.currentTurn.hasMeaningful = true
			for _, toolDelta := range choice.Delta.ToolCalls {
				s.currentTurn.appendToolDelta(toolDelta)
				s.engine.logParsedToolDelta(s.session.RunID, toolDelta)
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

func (s *llmRunStream) finishCurrentTurn() error {
	turn := s.currentTurn
	if turn == nil {
		return nil
	}
	s.currentTurn = nil
	if turn.body != nil {
		_ = turn.body.Close()
	}

	toolCalls := turn.materializeToolCalls(s.session.RunID)
	content := turn.content.String()
	if content != "" || len(toolCalls) > 0 {
		msg := openAIMessage{Role: "assistant"}
		if content != "" {
			msg.Content = content
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		s.messages = append(s.messages, msg)
	}

	if len(toolCalls) == 0 {
		if strings.TrimSpace(content) == "" {
			s.enqueueFallback("Model returned no assistant content.")
		}
		s.finished = true
		return nil
	}

	for _, toolCall := range toolCalls {
		toolEvents, toolMessage := s.executeToolCall(toolCall)
		s.pending = append(s.pending, toolEvents...)
		s.messages = append(s.messages, toolMessage)
	}
	return nil
}

func (s *llmRunStream) executeToolCall(toolCall openAIToolCall) ([]map[string]any, openAIMessage) {
	toolID := toolCall.ID
	if toolID == "" {
		toolID = s.session.RunID + "_tool_" + strings.ReplaceAll(toolCall.Function.Name, " ", "_")
	}

	args := map[string]any{}
	if strings.TrimSpace(toolCall.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			errorEvent := map[string]any{
				"type":      "tool.error",
				"runId":     s.session.RunID,
				"chatId":    s.session.ChatID,
				"toolId":    toolID,
				"toolName":  toolCall.Function.Name,
				"error":     "invalid_tool_arguments",
				"detail":    err.Error(),
				"timestamp": time.Now().UnixMilli(),
			}
			return []map[string]any{errorEvent}, openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + err.Error(),
			}
		}
	}

	events := []map[string]any{
		{
			"type":      "tool.start",
			"runId":     s.session.RunID,
			"chatId":    s.session.ChatID,
			"toolId":    toolID,
			"toolName":  toolCall.Function.Name,
			"toolType":  "backend",
			"timestamp": time.Now().UnixMilli(),
		},
	}
	if s.engine.cfg.SSE.IncludeToolPayloadEvents {
		events = append(events, map[string]any{
			"type":      "tool.snapshot",
			"runId":     s.session.RunID,
			"chatId":    s.session.ChatID,
			"toolId":    toolID,
			"toolName":  toolCall.Function.Name,
			"toolType":  "backend",
			"arguments": args,
			"timestamp": time.Now().UnixMilli(),
		})
	}

	result, invokeErr := s.engine.tools.Invoke(s.ctx, toolCall.Function.Name, args, s.execCtx)
	if invokeErr != nil {
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	if result.Error != "" {
		event := map[string]any{
			"type":      "tool.error",
			"runId":     s.session.RunID,
			"chatId":    s.session.ChatID,
			"toolId":    toolID,
			"toolName":  toolCall.Function.Name,
			"error":     result.Error,
			"timestamp": time.Now().UnixMilli(),
		}
		if s.engine.cfg.SSE.IncludeToolPayloadEvents {
			event["output"] = result.Output
		}
		events = append(events, event)
	} else {
		event := map[string]any{
			"type":      "tool.result",
			"runId":     s.session.RunID,
			"chatId":    s.session.ChatID,
			"toolId":    toolID,
			"toolName":  toolCall.Function.Name,
			"timestamp": time.Now().UnixMilli(),
		}
		if s.engine.cfg.SSE.IncludeToolPayloadEvents {
			event["output"] = result.StructuredOrOutput()
		}
		events = append(events, event)
	}

	return events, openAIMessage{
		Role:       "tool",
		ToolCallID: toolID,
		Name:       toolCall.Function.Name,
		Content:    result.Output,
	}
}

func (s *llmRunStream) enqueueFallback(text string) {
	if s.fallbackSent {
		return
	}
	s.fallbackSent = true
	s.pending = append(s.pending, s.newContentDeltaEvent(text))
}

func (s *llmRunStream) newContentDeltaEvent(delta string) map[string]any {
	event := map[string]any{
		"type":      "content.delta",
		"runId":     s.session.RunID,
		"chatId":    s.session.ChatID,
		"contentId": s.session.RunID + "_c_0",
		"delta":     delta,
		"index":     s.deltaIndex,
		"timestamp": time.Now().UnixMilli(),
	}
	s.deltaIndex++
	return event
}

func (t *providerTurnStream) appendToolDelta(delta openAIStreamToolDelta) {
	if t.toolCalls == nil {
		t.toolCalls = map[int]*toolCallAccumulator{}
	}
	acc, ok := t.toolCalls[delta.Index]
	if !ok {
		acc = &toolCallAccumulator{}
		t.toolCalls[delta.Index] = acc
	}
	if delta.ID != "" {
		acc.ID = delta.ID
	}
	if delta.Type != "" {
		acc.Type = delta.Type
	}
	if delta.Function.Name != "" {
		acc.FunctionName = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		acc.Arguments.WriteString(delta.Function.Arguments)
	}
}

func (t *providerTurnStream) materializeToolCalls(runID string) []openAIToolCall {
	if len(t.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(t.toolCalls))
	for idx := range t.toolCalls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]openAIToolCall, 0, len(t.toolCalls))
	for _, idx := range indexes {
		acc := t.toolCalls[idx]
		id := acc.ID
		if id == "" {
			id = fmt.Sprintf("%s_tool_%d", runID, idx)
		}
		toolType := acc.Type
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, openAIToolCall{
			ID:   id,
			Type: toolType,
			Function: openAIFunctionCall{
				Name:      acc.FunctionName,
				Arguments: acc.Arguments.String(),
			},
		})
	}
	return out
}

func (e *LLMAgentEngine) openProviderStream(ctx context.Context, provider ProviderDefinition, model ModelDefinition, messages []openAIMessage, toolSpecs []openAIToolSpec) (*providerTurnStream, error) {
	if provider.BaseURL == "" {
		return nil, fmt.Errorf("provider %s has empty baseUrl", provider.Key)
	}
	if provider.APIKey == "" {
		return nil, fmt.Errorf("provider %s has empty apiKey", provider.Key)
	}
	if model.Protocol != "" && model.Protocol != "OPENAI" {
		return nil, fmt.Errorf("streaming protocol %s is not supported", model.Protocol)
	}

	requestBody := openAIChatRequest{
		Model:       model.ModelID,
		Messages:    messages,
		Tools:       toolSpecs,
		ToolChoice:  "auto",
		Temperature: 0,
		Stream:      true,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(provider.BaseURL, "/") + provider.EndpointPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("model request failed with status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("model request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return &providerTurnStream{
		body:   resp.Body,
		reader: bufio.NewReader(resp.Body),
	}, nil
}

func (e *LLMAgentEngine) logRawChunk(runID string, chunk string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][raw_chunk] %s", runID, e.maskLogText(chunk))
}

func (e *LLMAgentEngine) logParsedDelta(runID string, kind string, value string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][parsed_%s] %s", runID, kind, e.maskLogText(value))
}

func (e *LLMAgentEngine) logParsedToolDelta(runID string, delta openAIStreamToolDelta) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf(
		"[llm][run:%s][parsed_tool_call] index=%d id=%s type=%s name=%s args=%s",
		runID,
		delta.Index,
		e.maskLogText(delta.ID),
		e.maskLogText(delta.Type),
		e.maskLogText(delta.Function.Name),
		e.maskLogText(delta.Function.Arguments),
	)
}

func (e *LLMAgentEngine) maskLogText(text string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(text), "\n", "\\n")
	if normalized == "" {
		return `""`
	}
	if e.cfg.Logging.LLMInteraction.MaskSensitive {
		return fmt.Sprintf("[masked chars=%d]", len(normalized))
	}
	const limit = 240
	if len(normalized) <= limit {
		return normalized
	}
	return normalized[:limit] + "...[truncated]"
}

func readSSEData(reader *bufio.Reader) (string, error) {
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 {
				return strings.Join(dataLines, "\n"), nil
			}
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 {
				return "", io.EOF
			}
			return strings.Join(dataLines, "\n"), nil
		}
	}
}

func (r ToolExecutionResult) StructuredOrOutput() any {
	if len(r.Structured) > 0 {
		return r.Structured
	}
	return r.Output
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

func buildSystemPrompt(session QuerySession, req api.QueryRequest, modelKey string) string {
	var builder strings.Builder
	builder.WriteString("You are the Go runner agent.\n")
	if session.AgentName != "" {
		builder.WriteString("Current agentName: " + session.AgentName + "\n")
	}
	builder.WriteString("Current runId: " + session.RunID + "\n")
	builder.WriteString("Current chatId: " + session.ChatID + "\n")
	builder.WriteString("Current agentKey: " + session.AgentKey + "\n")
	builder.WriteString("Current modelKey: " + modelKey + "\n")
	if session.Mode != "" {
		builder.WriteString("Current mode: " + session.Mode + "\n")
	}
	if session.Subject != "" {
		builder.WriteString("Current subject: " + session.Subject + "\n")
	}
	if len(req.References) > 0 {
		builder.WriteString("References:\n")
		for _, ref := range req.References {
			builder.WriteString("- ")
			builder.WriteString(ref.Name)
			if ref.SandboxPath != "" {
				builder.WriteString(" sandboxPath=" + ref.SandboxPath)
			}
			builder.WriteString("\n")
		}
	}
	builder.WriteString("Use available tools when they are necessary, and provide a final assistant answer after tool use.")
	return builder.String()
}

func filterToolDefinitions(defs []api.ToolDetailResponse, allowed []string) []api.ToolDetailResponse {
	if len(allowed) == 0 {
		return defs
	}
	allowedSet := map[string]struct{}{}
	for _, name := range allowed {
		if strings.TrimSpace(name) != "" {
			allowedSet[strings.TrimSpace(name)] = struct{}{}
		}
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if _, ok := allowedSet[def.Name]; ok {
			filtered = append(filtered, def)
			continue
		}
		if _, ok := allowedSet[def.Key]; ok {
			filtered = append(filtered, def)
		}
	}
	return filtered
}
