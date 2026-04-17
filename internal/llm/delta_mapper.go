package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/stream"
)

type DeltaMapper struct {
	runID                string
	chatID               string
	toolTimeoutMs        int64
	reasoningSeq         int
	contentSeq           int
	activeReasoningID    string
	activeContentID      string
	lastKind             string
	indexedToolIDs       map[int]string
	toolArgChunkCounters map[string]int
	toolArgBuffers       map[string]*strings.Builder
	actionToolIDs        map[string]bool
	toolRegistry         ToolDefinitionLookup
	frontend             *frontendtools.Registry
}

func NewDeltaMapper(runID string, chatID string, toolTimeoutMs int64, toolRegistry ToolDefinitionLookup, frontend *frontendtools.Registry) *DeltaMapper {
	return &DeltaMapper{
		runID:                runID,
		chatID:               chatID,
		toolTimeoutMs:        toolTimeoutMs,
		indexedToolIDs:       map[int]string{},
		toolArgChunkCounters: map[string]int{},
		toolArgBuffers:       map[string]*strings.Builder{},
		actionToolIDs:        map[string]bool{},
		toolRegistry:         toolRegistry,
		frontend:             frontend,
	}
}

func (m *DeltaMapper) Map(delta AgentDelta) []stream.StreamInput {
	switch value := delta.(type) {
	case DeltaContent:
		contentID := value.ContentID
		if contentID == "" {
			if m.activeContentID == "" || m.lastKind != "content" {
				m.contentSeq++
				m.activeContentID = fmt.Sprintf("%s_c_%d", m.runID, m.contentSeq)
			}
			contentID = m.activeContentID
		} else {
			m.activeContentID = contentID
		}
		m.lastKind = "content"
		return []stream.StreamInput{stream.ContentDelta{
			ContentID: contentID,
			Delta:     value.Text,
		}}
	case DeltaReasoning:
		reasoningID := value.ReasoningID
		if reasoningID == "" {
			if m.activeReasoningID == "" || m.lastKind != "reasoning" {
				m.reasoningSeq++
				m.activeReasoningID = fmt.Sprintf("%s_r_%d", m.runID, m.reasoningSeq)
			}
			reasoningID = m.activeReasoningID
		} else {
			m.activeReasoningID = reasoningID
		}
		m.lastKind = "reasoning"
		return []stream.StreamInput{stream.ReasoningDelta{
			ReasoningID:    reasoningID,
			ReasoningLabel: value.ReasoningLabel,
			Delta:          value.Text,
		}}
	case DeltaToolCall:
		toolID := m.resolveToolID(value.Index, value.ID, value.Name)
		if toolID == "" {
			return nil
		}
		viewportType, toolLabel, toolDescription := m.resolveToolMetadata(value.Name)
		if viewportType == "action" {
			m.actionToolIDs[toolID] = true
			m.lastKind = "action"
			return []stream.StreamInput{stream.ActionArgs{
				ActionID:    toolID,
				Delta:       value.ArgsDelta,
				ActionName:  value.Name,
				Description: toolDescription,
			}}
		}
		chunkIndex := m.toolArgChunkCounters[toolID]
		m.toolArgChunkCounters[toolID] = chunkIndex + 1
		m.lastKind = "tool"
		awaitAsk, emitAwaitBeforeToolArgs := m.buildFrontendToolAwaitAsk(toolID, value.Name, value.ArgsDelta, chunkIndex)
		toolArgs := stream.ToolArgs{
			ToolID:          toolID,
			Delta:           value.ArgsDelta,
			ToolName:        value.Name,
			ToolLabel:       toolLabel,
			ToolDescription: toolDescription,
			ChunkIndex:      chunkIndex,
			AwaitAsk:        awaitAsk,
		}
		if emitAwaitBeforeToolArgs && awaitAsk != nil {
			toolArgs.AwaitAsk = nil
			return []stream.StreamInput{*awaitAsk, toolArgs}
		}
		return []stream.StreamInput{toolArgs}
	case DeltaToolEnd:
		m.lastKind = ""
		inputs := make([]stream.StreamInput, 0, len(value.ToolIDs))
		for _, toolID := range value.ToolIDs {
			delete(m.toolArgBuffers, toolID)
			if m.actionToolIDs[toolID] {
				inputs = append(inputs, stream.ActionEnd{ActionID: toolID})
				continue
			}
			inputs = append(inputs, stream.ToolEnd{ToolID: toolID})
		}
		return inputs
	case DeltaToolResult:
		m.lastKind = ""
		_, toolLabel, toolDescription := m.resolveToolMetadata(value.ToolName)
		if m.actionToolIDs[value.ToolID] {
			return []stream.StreamInput{stream.ActionResult{
				ActionID:    value.ToolID,
				ActionName:  value.ToolName,
				Description: toolDescription,
				Result:      structuredOrOutput(value.Result),
			}}
		}
		return []stream.StreamInput{stream.ToolResult{
			ToolID:          value.ToolID,
			ToolName:        value.ToolName,
			ToolLabel:       toolLabel,
			ToolDescription: toolDescription,
			Result:          sseResultValue(value.Result),
			Error:           value.Result.Error,
			ExitCode:        value.Result.ExitCode,
		}}
	case DeltaStageMarker:
		m.lastKind = ""
		return []stream.StreamInput{stream.StageMarker{Stage: value.Stage}}
	case DeltaFinishReason:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputRunComplete{FinishReason: value.Reason}}
	case DeltaError:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputRunError{Error: value.Error}}
	case DeltaPlanUpdate:
		m.lastKind = ""
		return []stream.StreamInput{stream.PlanUpdate{
			PlanID: value.PlanID,
			Plan:   value.Plan,
			ChatID: value.ChatID,
		}}
	case DeltaTaskLifecycle:
		m.lastKind = ""
		switch strings.ToLower(value.Kind) {
		case "start":
			return []stream.StreamInput{stream.TaskStart{
				TaskID:      value.TaskID,
				RunID:       value.RunID,
				TaskName:    value.TaskName,
				Description: value.Description,
			}}
		case "complete":
			return []stream.StreamInput{stream.TaskComplete{TaskID: value.TaskID}}
		case "cancel":
			return []stream.StreamInput{stream.TaskCancel{TaskID: value.TaskID}}
		case "fail":
			return []stream.StreamInput{stream.TaskFail{TaskID: value.TaskID, Error: value.Error}}
		default:
			return nil
		}
	case DeltaArtifactPublish:
		return []stream.StreamInput{stream.ArtifactPublish{
			ArtifactID: value.ArtifactID,
			ChatID:     value.ChatID,
			RunID:      value.RunID,
			Artifact:   value.Artifact,
		}}
	case DeltaAwaitAsk:
		return []stream.StreamInput{stream.AwaitAsk{
			AwaitingID:   value.AwaitingID,
			ViewportType: value.ViewportType,
			ViewportKey:  value.ViewportKey,
			Mode:         value.Mode,
			Timeout:      value.Timeout,
			RunID:        value.RunID,
			Questions:    append([]any(nil), value.Questions...),
		}}
	case DeltaAwaitPayload:
		return []stream.StreamInput{stream.AwaitPayload{
			AwaitingID: value.AwaitingID,
			Questions:  append([]any(nil), value.Questions...),
		}}
	case DeltaRequestSubmit:
		return []stream.StreamInput{stream.RequestSubmit{
			RequestID:  value.RequestID,
			ChatID:     value.ChatID,
			RunID:      value.RunID,
			AwaitingID: value.AwaitingID,
			Params:     value.Params,
		}}
	case DeltaAwaitingAnswer:
		return []stream.StreamInput{stream.AwaitingAnswer{
			AwaitingID: value.AwaitingID,
			Answer:     CloneMap(value.Answer),
		}}
	case DeltaRequestSteer:
		return []stream.StreamInput{stream.RequestSteer{
			RequestID: value.RequestID,
			ChatID:    value.ChatID,
			RunID:     value.RunID,
			SteerID:   value.SteerID,
			Message:   value.Message,
		}}
	case DeltaDebugPreCall:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputDebugPreCall{
			ChatID:                value.ChatID,
			ModelKey:              value.ModelKey,
			ContextWindow:         value.ContextWindow,
			CurrentContextSize:    value.CurrentContextSize,
			EstimatedNextCallSize: value.EstimatedNextCallSize,
			RunPromptTokens:       value.RunPromptTokens,
			RunCompletionTokens:   value.RunCompletionTokens,
			RunTotalTokens:        value.RunTotalTokens,
		}}
	case DeltaDebugPostCall:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputDebugPostCall{
			ChatID:                    value.ChatID,
			ModelKey:                  value.ModelKey,
			ContextWindow:             value.ContextWindow,
			CurrentContextSize:        value.CurrentContextSize,
			EstimatedNextCallSize:     value.EstimatedNextCallSize,
			LLMReturnPromptTokens:     value.LLMReturnPromptTokens,
			LLMReturnCompletionTokens: value.LLMReturnCompletionTokens,
			LLMReturnTotalTokens:      value.LLMReturnTotalTokens,
			RunPromptTokens:           value.RunPromptTokens,
			RunCompletionTokens:       value.RunCompletionTokens,
			RunTotalTokens:            value.RunTotalTokens,
		}}
	case DeltaRunCancel:
		return []stream.StreamInput{stream.RunCancel{RunID: value.RunID}}
	default:
		return nil
	}
}

func (m *DeltaMapper) buildFrontendToolAwaitAsk(toolID string, toolName string, argsDelta string, chunkIndex int) (*stream.AwaitAsk, bool) {
	if m.frontend == nil || m.toolRegistry == nil {
		return nil, false
	}
	tool, ok := m.toolRegistry.Tool(toolName)
	if !ok {
		return nil, false
	}
	kind, _ := tool.Meta["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "frontend") {
		return nil, false
	}
	if !m.isClientVisibleTool(toolName) {
		return nil, false
	}
	handler, ok := m.frontend.Handler(toolName)
	if !ok {
		return nil, false
	}
	if chunkIndex > 0 {
		buffer := m.toolArgBuffers[toolID]
		if buffer == nil {
			return nil, false
		}
		buffer.WriteString(argsDelta)
		var args map[string]any
		if err := json.Unmarshal([]byte(buffer.String()), &args); err != nil {
			return nil, false
		}
		if err := handler.ValidateArgs(args); err != nil {
			delete(m.toolArgBuffers, toolID)
			return nil, false
		}
		delete(m.toolArgBuffers, toolID)
		return handler.BuildInitialAwaitAsk(toolID, m.runID, tool, 0, m.toolTimeoutMs), true
	}
	if strings.TrimSpace(argsDelta) != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(argsDelta), &args); err == nil {
			if err := handler.ValidateArgs(args); err == nil {
				return handler.BuildInitialAwaitAsk(toolID, m.runID, tool, chunkIndex, m.toolTimeoutMs), false
			}
			return nil, false
		}
		buffer := &strings.Builder{}
		buffer.WriteString(argsDelta)
		m.toolArgBuffers[toolID] = buffer
		return nil, false
	}
	return handler.BuildInitialAwaitAsk(toolID, m.runID, tool, chunkIndex, m.toolTimeoutMs), false
}

func (m *DeltaMapper) resolveToolID(index int, candidate string, toolName string) string {
	if strings.TrimSpace(candidate) != "" {
		m.indexedToolIDs[index] = candidate
		return candidate
	}
	if value := strings.TrimSpace(m.indexedToolIDs[index]); value != "" {
		return value
	}
	return ""
}

func (m *DeltaMapper) resolveToolMetadata(toolName string) (string, string, string) {
	if m.toolRegistry == nil {
		return "", "", ""
	}
	tool, ok := m.toolRegistry.Tool(toolName)
	if !ok {
		return "", "", ""
	}

	kind, _ := tool.Meta["kind"].(string)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "action":
		return "action", tool.Label, tool.Description
	case "frontend":
		viewportType, _ := tool.Meta["viewportType"].(string)
		return strings.TrimSpace(viewportType), tool.Label, tool.Description
	default:
		return "", tool.Label, tool.Description
	}
}

func (m *DeltaMapper) resolveViewportMetadata(toolName string) (string, string) {
	if m.toolRegistry == nil {
		return "", ""
	}
	tool, ok := m.toolRegistry.Tool(toolName)
	if !ok {
		return "", ""
	}
	viewportType, _ := tool.Meta["viewportType"].(string)
	viewportKey, _ := tool.Meta["viewportKey"].(string)
	return strings.TrimSpace(viewportType), strings.TrimSpace(viewportKey)
}

func (m *DeltaMapper) isClientVisibleTool(toolName string) bool {
	if m.toolRegistry == nil {
		return true
	}
	tool, ok := m.toolRegistry.Tool(toolName)
	if !ok {
		return true
	}
	if clientVisible, ok := tool.Meta["clientVisible"].(bool); ok {
		return clientVisible
	}
	return true
}
