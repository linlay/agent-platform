package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/stream"
)

type DeltaMapper struct {
	runID                string
	chatID               string
	budget               Budget
	reasoningSeq         int
	contentSeq           int
	activeReasoningID    string
	activeContentID      string
	lastKind             string
	indexedToolIDs       map[int]string
	toolArgChunkCounters map[string]int
	toolArgBuffers       map[string]*strings.Builder
	pendingToolAwaitAsks map[string]*stream.AwaitAsk
	actionToolIDs        map[string]bool
	toolRegistry         ToolDefinitionLookup
	frontend             *frontendtools.Registry
}

func NewDeltaMapper(runID string, chatID string, budget Budget, toolRegistry ToolDefinitionLookup, frontend *frontendtools.Registry) *DeltaMapper {
	return &DeltaMapper{
		runID:                runID,
		chatID:               chatID,
		budget:               budget,
		indexedToolIDs:       map[int]string{},
		toolArgChunkCounters: map[string]int{},
		toolArgBuffers:       map[string]*strings.Builder{},
		pendingToolAwaitAsks: map[string]*stream.AwaitAsk{},
		actionToolIDs:        map[string]bool{},
		toolRegistry:         toolRegistry,
		frontend:             frontend,
	}
}

func (m *DeltaMapper) CloneIsolated(runID string, chatID string) StreamDeltaMapper {
	if m == nil {
		return nil
	}
	return NewDeltaMapper(runID, chatID, m.budget, m.toolRegistry, m.frontend)
}

type DeltaMapperFactory struct {
	Frontend *frontendtools.Registry
}

func (f DeltaMapperFactory) NewDeltaMapper(runID string, chatID string, budget Budget, toolRegistry ToolDefinitionLookup) StreamDeltaMapper {
	return NewDeltaMapper(runID, chatID, budget, toolRegistry, f.Frontend)
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
		if awaitAsk != nil && strings.EqualFold(strings.TrimSpace(value.Name), "ask_user_question") {
			m.pendingToolAwaitAsks[toolID] = awaitAsk
			awaitAsk = nil
			emitAwaitBeforeToolArgs = false
		}
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
			inputs = append(inputs, stream.ToolEnd{
				ToolID:     toolID,
				FileChange: CloneMap(value.FileChanges[toolID]),
			})
			if awaitAsk := m.pendingToolAwaitAsks[toolID]; awaitAsk != nil {
				inputs = append(inputs, *awaitAsk)
				delete(m.pendingToolAwaitAsks, toolID)
			}
		}
		return inputs
	case DeltaToolResult:
		m.lastKind = ""
		_, toolLabel, toolDescription := m.resolveToolMetadata(value.ToolName)
		sseResult := sseResultValue(value.Result)
		resultError := value.Result.Error
		resultExitCode := value.Result.ExitCode
		if isBashTool(value.ToolName) && len(value.Result.Structured) > 0 {
			resultError = ""
			resultExitCode = 0
		}
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
			Result:          sseResult,
			Hitl:            CloneMap(value.Result.HITL),
			Error:           resultError,
			ExitCode:        resultExitCode,
			FileChange:      toolResultFileChange(value.ToolName, value.Result),
		}}
	case DeltaStageMarker:
		m.lastKind = ""
		return []stream.StreamInput{stream.StageMarker{Stage: value.Stage}}
	case DeltaSyntheticQuery:
		m.lastKind = ""
		return []stream.StreamInput{stream.SyntheticQuery{
			ChatID:   value.ChatID,
			Role:     value.Role,
			Message:  value.Message,
			Messages: cloneRawMessageMaps(value.Messages),
			Systems:  cloneRawMessageMaps(value.Systems),
		}}
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
	case DeltaPlanningStart:
		m.lastKind = ""
		return []stream.StreamInput{stream.PlanningStart{
			PlanningID: value.PlanningID,
		}}
	case DeltaPlanningDelta:
		m.lastKind = ""
		return []stream.StreamInput{stream.PlanningDelta{
			PlanningID: value.PlanningID,
			Delta:      value.Delta,
		}}
	case DeltaPlanningEnd:
		m.lastKind = ""
		return []stream.StreamInput{stream.PlanningEnd{
			PlanningID: value.PlanningID,
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
				SubAgentKey: value.SubAgentKey,
				MainToolID:  value.MainToolID,
			}}
		case "complete":
			return []stream.StreamInput{stream.TaskComplete{TaskID: value.TaskID}}
		case "cancel":
			return []stream.StreamInput{stream.TaskCancel{TaskID: value.TaskID, Reason: value.Reason}}
		case "error":
			return []stream.StreamInput{stream.TaskError{TaskID: value.TaskID, Error: value.Error}}
		default:
			return nil
		}
	case DeltaArtifactPublish:
		return []stream.StreamInput{stream.ArtifactPublish{
			ChatID:        value.ChatID,
			RunID:         value.RunID,
			ArtifactCount: value.ArtifactCount,
			Artifacts:     append([]map[string]any(nil), value.Artifacts...),
		}}
	case DeltaSourcePublish:
		return []stream.StreamInput{stream.SourcePublish{
			PublishID: value.PublishID,
			RunID:     value.RunID,
			ToolID:    value.ToolID,
			Kind:      value.Kind,
			Query:     value.Query,
			Sources:   append([]stream.Source(nil), value.Sources...),
		}}
	case DeltaAwaitAsk:
		return []stream.StreamInput{stream.AwaitAsk{
			AwaitingID:   value.AwaitingID,
			Mode:         value.Mode,
			Timeout:      value.Timeout,
			RunID:        value.RunID,
			ViewportType: value.ViewportType,
			ViewportKey:  value.ViewportKey,
			Questions:    append([]any(nil), value.Questions...),
			Approvals:    append([]any(nil), value.Approvals...),
			Forms:        append([]any(nil), value.Forms...),
			Plan:         CloneMap(value.Plan),
		}}
	case DeltaRequestSubmit:
		return []stream.StreamInput{stream.RequestSubmit{
			RequestID:  value.RequestID,
			ChatID:     value.ChatID,
			RunID:      value.RunID,
			AwaitingID: value.AwaitingID,
			SubmitID:   value.SubmitID,
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
	case DeltaLLMRequest:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputLLMRequest{
			TaskID:          value.TaskID,
			ChatID:          value.ChatID,
			Model:           CloneMap(value.Model),
			ModelKey:        value.ModelKey,
			ReasoningEffort: value.ReasoningEffort,
			System:          CloneMap(value.System),
			SystemRef:       CloneMap(value.SystemRef),
			ToolChoice:      value.ToolChoice,
			RequestOptions:  CloneMap(value.RequestOptions),
			InputMessages:   cloneRawMessageMaps(value.InputMessages),
		}}
	case DeltaDebugLLMChat:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputDebugLLMChat{
			TaskID:                          value.TaskID,
			ChatID:                          value.ChatID,
			ProviderKey:                     value.ProviderKey,
			ProviderEndpoint:                value.ProviderEndpoint,
			ModelKey:                        value.ModelKey,
			ModelID:                         value.ModelID,
			ReasoningEffort:                 value.ReasoningEffort,
			Status:                          value.Status,
			RunSeq:                          value.RunSeq,
			TraceFile:                       value.TraceFile,
			TraceURL:                        value.TraceURL,
			SystemRef:                       CloneMap(value.SystemRef),
			ContextWindow:                   value.ContextWindow,
			CurrentContextSize:              value.CurrentContextSize,
			EstimatedNextCallSize:           value.EstimatedNextCallSize,
			LLMReturnPromptTokens:           value.LLMReturnPromptTokens,
			LLMReturnCompletionTokens:       value.LLMReturnCompletionTokens,
			LLMReturnTotalTokens:            value.LLMReturnTotalTokens,
			LLMReturnCachedTokens:           value.LLMReturnCachedTokens,
			LLMReturnReasoningTokens:        value.LLMReturnReasoningTokens,
			LLMReturnPromptCacheHitTokens:   value.LLMReturnPromptCacheHitTokens,
			LLMReturnPromptCacheMissTokens:  value.LLMReturnPromptCacheMissTokens,
			LLMReturnLLMChatCompletionCount: value.LLMReturnLLMChatCompletionCount,
			LLMReturnToolCallCount:          value.LLMReturnToolCallCount,
			LLMReturnFirstTokenLatencyMs:    value.LLMReturnFirstTokenLatencyMs,
			LLMReturnGenerationDurationMs:   value.LLMReturnGenerationDurationMs,
			RunPromptTokens:                 value.RunPromptTokens,
			RunCompletionTokens:             value.RunCompletionTokens,
			RunTotalTokens:                  value.RunTotalTokens,
			RunCachedTokens:                 value.RunCachedTokens,
			RunReasoningTokens:              value.RunReasoningTokens,
			RunPromptCacheHitTokens:         value.RunPromptCacheHitTokens,
			RunPromptCacheMissTokens:        value.RunPromptCacheMissTokens,
			RunLLMChatCompletionCount:       value.RunLLMChatCompletionCount,
			RunToolCallCount:                value.RunToolCallCount,
			RunFirstTokenLatencyTotalMs:     value.RunFirstTokenLatencyTotalMs,
			RunFirstTokenLatencyCount:       value.RunFirstTokenLatencyCount,
			RunGenerationDurationMs:         value.RunGenerationDurationMs,
		}}
	case DeltaUsageSnapshot:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputUsageSnapshot{
			ChatID:                          value.ChatID,
			ModelKey:                        value.ModelKey,
			ReasoningEffort:                 value.ReasoningEffort,
			ContextWindow:                   value.ContextWindow,
			CurrentContextSize:              value.CurrentContextSize,
			EstimatedNextCallSize:           value.EstimatedNextCallSize,
			LLMReturnPromptTokens:           value.LLMReturnPromptTokens,
			LLMReturnCompletionTokens:       value.LLMReturnCompletionTokens,
			LLMReturnTotalTokens:            value.LLMReturnTotalTokens,
			LLMReturnCachedTokens:           value.LLMReturnCachedTokens,
			LLMReturnReasoningTokens:        value.LLMReturnReasoningTokens,
			LLMReturnPromptCacheHitTokens:   value.LLMReturnPromptCacheHitTokens,
			LLMReturnPromptCacheMissTokens:  value.LLMReturnPromptCacheMissTokens,
			LLMReturnLLMChatCompletionCount: value.LLMReturnLLMChatCompletionCount,
			LLMReturnToolCallCount:          value.LLMReturnToolCallCount,
			LLMReturnFirstTokenLatencyMs:    value.LLMReturnFirstTokenLatencyMs,
			LLMReturnGenerationDurationMs:   value.LLMReturnGenerationDurationMs,
			RunPromptTokens:                 value.RunPromptTokens,
			RunCompletionTokens:             value.RunCompletionTokens,
			RunTotalTokens:                  value.RunTotalTokens,
			RunCachedTokens:                 value.RunCachedTokens,
			RunReasoningTokens:              value.RunReasoningTokens,
			RunPromptCacheHitTokens:         value.RunPromptCacheHitTokens,
			RunPromptCacheMissTokens:        value.RunPromptCacheMissTokens,
			RunLLMChatCompletionCount:       value.RunLLMChatCompletionCount,
			RunToolCallCount:                value.RunToolCallCount,
			RunFirstTokenLatencyTotalMs:     value.RunFirstTokenLatencyTotalMs,
			RunFirstTokenLatencyCount:       value.RunFirstTokenLatencyCount,
			RunGenerationDurationMs:         value.RunGenerationDurationMs,
		}}
	case DeltaRunActivity:
		m.lastKind = ""
		return []stream.StreamInput{stream.InputRunActivity{
			TaskID:      value.TaskID,
			ChatID:      value.ChatID,
			Phase:       value.Phase,
			Status:      value.Status,
			Backend:     value.Backend,
			Key:         value.Key,
			Message:     value.Message,
			Retry:       CloneMap(value.Retry),
			Recovery:    CloneMap(value.Recovery),
			Degradation: CloneMap(value.Degradation),
		}}
	case DeltaRunCancel:
		return []stream.StreamInput{stream.RunCancel{RunID: value.RunID}}
	default:
		return nil
	}
}

func cloneRawMessageMaps(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, CloneMap(msg))
	}
	return out
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
		timeout := resolveFrontendAwaitTimeout(toolName, tool, args, m.budget)
		return handler.BuildInitialAwaitAsk(toolID, m.runID, tool, args, 0, timeout), true
	}
	if strings.TrimSpace(argsDelta) != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(argsDelta), &args); err == nil {
			if err := handler.ValidateArgs(args); err == nil {
				timeout := resolveFrontendAwaitTimeout(toolName, tool, args, m.budget)
				return handler.BuildInitialAwaitAsk(toolID, m.runID, tool, args, chunkIndex, timeout), false
			}
			return nil, false
		}
		buffer := &strings.Builder{}
		buffer.WriteString(argsDelta)
		m.toolArgBuffers[toolID] = buffer
		return nil, false
	}
	return nil, false
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
