package llm

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
)

func (s *llmRunStream) appendCompatReasoningFromOpenAI(reasoningContent string, reasoningDetails []map[string]any) {
	switch s.responseReasoningFormat() {
	case "REASONING_DETAILS_TEXT":
		for _, text := range extractReasoningDetailTexts(reasoningDetails) {
			s.appendReasoningDelta(text, "reasoning_details")
		}
	case "REASONING_CONTENT":
		s.appendReasoningDelta(reasoningContent, "reasoning_content")
	}
}

func (s *llmRunStream) appendCompatAnthropicThinking(thinking string) {
	if s.responseReasoningFormat() != "ANTHROPIC_THINKING_DELTA" {
		return
	}
	s.appendReasoningDelta(thinking, "thinking_delta")
}

func (s *llmRunStream) appendCompatContent(text string) {
	if text == "" {
		return
	}
	if s.responseReasoningFormat() == "THINK_TAG_CONTENT" {
		s.appendThinkTagContent(text, false)
		return
	}
	s.appendContentDelta(text)
}

func (s *llmRunStream) appendContentDelta(text string) {
	if text == "" {
		return
	}
	s.currentTurn.hasMeaningful = true
	s.currentTurn.content.WriteString(text)
	s.engine.logParsedDelta(s.session.RunID, "content", text)
	s.pending = append(s.pending, s.newContentDeltaEvent(text))
}

func (s *llmRunStream) appendReasoningDelta(text string, label string) {
	if text == "" {
		return
	}
	s.currentTurn.hasMeaningful = true
	s.currentTurn.reasoning.WriteString(text)
	s.engine.logParsedDelta(s.session.RunID, label, text)
	s.pending = append(s.pending, DeltaReasoning{Text: text, ReasoningLabel: label})
}

func (s *llmRunStream) appendThinkTagContent(chunk string, flush bool) {
	const (
		startTag = "<think>"
		endTag   = "</think>"
	)

	s.currentTurn.hasMeaningful = true
	parser := &s.currentTurn.thinkTag
	parser.buffer.WriteString(chunk)
	for {
		pending := parser.buffer.String()
		if parser.inThink {
			index := strings.Index(pending, endTag)
			if index >= 0 {
				s.appendReasoningDelta(pending[:index], "think_tag")
				parser.buffer.Reset()
				parser.buffer.WriteString(pending[index+len(endTag):])
				parser.inThink = false
				continue
			}
			if !flush {
				flushLen := len(pending) - (len(endTag) - 1)
				if flushLen <= 0 {
					return
				}
				s.appendReasoningDelta(pending[:flushLen], "think_tag")
				parser.buffer.Reset()
				parser.buffer.WriteString(pending[flushLen:])
				return
			}
			s.appendReasoningDelta(pending, "think_tag")
			parser.buffer.Reset()
			return
		}

		index := strings.Index(pending, startTag)
		if index >= 0 {
			s.appendContentDelta(pending[:index])
			parser.buffer.Reset()
			parser.buffer.WriteString(pending[index+len(startTag):])
			parser.inThink = true
			continue
		}
		if !flush {
			flushLen := len(pending) - (len(startTag) - 1)
			if flushLen <= 0 {
				return
			}
			s.appendContentDelta(pending[:flushLen])
			parser.buffer.Reset()
			parser.buffer.WriteString(pending[flushLen:])
			return
		}
		s.appendContentDelta(pending)
		parser.buffer.Reset()
		return
	}
}

func (s *llmRunStream) responseReasoningFormat() string {
	format := AnyStringNode(AnyMapNode(s.protocolConfig.Compat["response"])["reasoningFormat"])
	if format == "" {
		switch strings.ToUpper(strings.TrimSpace(s.model.Protocol)) {
		case "ANTHROPIC":
			return "ANTHROPIC_THINKING_DELTA"
		default:
			return "REASONING_CONTENT"
		}
	}
	return strings.ToUpper(strings.TrimSpace(format))
}

func extractReasoningDetailTexts(details []map[string]any) []string {
	texts := make([]string, 0, len(details))
	for _, detail := range details {
		if text := AnyStringNode(detail["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func (s *llmRunStream) newContentDeltaEvent(delta string) AgentDelta {
	return DeltaContent{Text: delta}
}

func (t *providerTurnStream) appendOpenAIToolDelta(delta openAIStreamToolDelta) []AgentDelta {
	return t.appendToolCallDelta(delta.Index, delta.ID, delta.Type, delta.Function.Name, delta.Function.Arguments)
}

func (t *providerTurnStream) appendToolCallDelta(index int, toolID string, toolType string, toolName string, argumentsChunk string) []AgentDelta {
	if t.toolCalls == nil {
		t.toolCalls = map[int]*toolCallAccumulator{}
	}
	acc, ok := t.toolCalls[index]
	if !ok {
		acc = &toolCallAccumulator{}
		t.toolCalls[index] = acc
	}
	if toolID != "" {
		acc.ID = toolID
	}
	if toolType != "" {
		acc.Type = toolType
	}
	if toolName != "" {
		acc.FunctionName = toolName
	}
	if argumentsChunk != "" {
		acc.Arguments.WriteString(argumentsChunk)
	}
	if acc.ID == "" {
		return nil
	}
	arguments := acc.Arguments.String()
	if len(arguments) <= acc.EmittedBytes {
		return nil
	}
	argsDelta := arguments[acc.EmittedBytes:]
	acc.EmittedBytes = len(arguments)
	return []AgentDelta{DeltaToolCall{
		Index:     index,
		ID:        acc.ID,
		Name:      acc.FunctionName,
		ArgsDelta: argsDelta,
	}}
}

func (t *providerTurnStream) materializeToolCalls() ([]openAIToolCall, error) {
	if len(t.toolCalls) == 0 {
		return nil, nil
	}
	indexes := make([]int, 0, len(t.toolCalls))
	for idx := range t.toolCalls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]openAIToolCall, 0, len(t.toolCalls))
	for _, idx := range indexes {
		acc := t.toolCalls[idx]
		if strings.TrimSpace(acc.ID) == "" {
			return nil, fmt.Errorf("provider tool call missing toolCallId for index %d", idx)
		}
		toolType := acc.Type
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, openAIToolCall{
			ID:   acc.ID,
			Type: toolType,
			Function: openAIFunctionCall{
				Name:      acc.FunctionName,
				Arguments: acc.Arguments.String(),
			},
		})
	}
	return out, nil
}

func readSSEFrame(reader *bufio.Reader) (string, string, error) {
	var eventName string
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 || eventName != "" {
				return eventName, strings.Join(dataLines, "\n"), nil
			}
			if errors.Is(err, io.EOF) {
				return "", "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				return "", "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 && eventName == "" {
				return "", "", io.EOF
			}
			return eventName, strings.Join(dataLines, "\n"), nil
		}
	}
}

func formatRawSSEFrame(eventName string, rawChunk string) string {
	if strings.TrimSpace(eventName) == "" {
		return rawChunk
	}
	return "event: " + strings.TrimSpace(eventName) + "\ndata: " + rawChunk
}
