package chat

import (
	"strings"
	"time"

	"agent-platform/internal/stream"
)

// StepWriter accumulates SSE events and writes Java-compatible JSONL lines
// (_type: "query" / "react" / "react-tool" / "plan-execute" / "submit" / "steer" / "event")
// to the chat store.
//
// It mirrors the behaviour of Java's TurnTraceWriter:
//   - internal stage markers flush the current step and start a new one
//   - plan/artifact state is tracked and attached to step lines
//   - snapshot events (reasoning/content/tool/action) become StoredMessages
//   - request.submit + awaiting.answer are merged into SubmitLines
//   - request.steer becomes a typed EventLine so chat detail can replay it
type StepWriter struct {
	store  Store
	chatID string
	runID  string
	mode   string // "REACT" / "PLAN_EXECUTE" / "ONESHOT" / "CODER"

	queryWritten bool
	seqCounter   int

	currentStage string

	messages       []StoredMessage
	latestPlan     *PlanState
	latestArtifact *ArtifactState
	taskBuffers    map[string]*taskStepBuffer
	closedTaskIDs  map[string]bool
	stepLiveSeq    int64

	// tool/action name tracking (for tool.result → StoredMessage.Name)
	toolNames     map[string]string
	actionNames   map[string]string
	toolTaskIDs   map[string]string
	actionTaskIDs map[string]string

	// msgId generation
	currentMsgID string
	needNewMsgID bool

	// pending step-level metadata captured during the current LLM turn
	pendingAwaiting         []map[string]any
	pendingApproval         *StepApproval
	pendingSubmit           map[string]any
	pendingSubmitLiveSeq    int64
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingEstimated        int
	pendingModelKey         string
	pendingReasoningEffort  string
	pendingSystemRef        map[string]any
	pendingSystemInits      []QueryLineSystemInit
}

type StepWriterOption func(*StepWriter)

// NewStepWriter creates a StepWriter for a single run.
func NewStepWriter(store Store, chatID, runID, mode string, opts ...StepWriterOption) *StepWriter {
	w := &StepWriter{
		store:         store,
		chatID:        chatID,
		runID:         runID,
		mode:          strings.ToUpper(strings.TrimSpace(mode)),
		taskBuffers:   map[string]*taskStepBuffer{},
		closedTaskIDs: map[string]bool{},
		toolNames:     map[string]string{},
		actionNames:   map[string]string{},
		toolTaskIDs:   map[string]string{},
		actionTaskIDs: map[string]string{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

func (w *StepWriter) SetPendingSystemInits(lines []QueryLineSystemInit) {
	if w == nil || len(lines) == 0 {
		return
	}
	w.pendingSystemInits = append([]QueryLineSystemInit(nil), lines...)
}

// OnEvent processes a single SSE event from the stream.
// It should be called for every event that goes through writeEvent in server.go.
func (w *StepWriter) OnEvent(event stream.EventData) {
	switch event.Type {
	case "request.query":
		w.handleRequestQuery(event)

	case "run.start":
		// default stage; will be overridden by the first internal stage marker
		if w.currentStage == "" {
			w.currentStage = "oneshot"
		}

	case "reasoning.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		ts := event.Timestamp
		w.appendStoredMessage(event, StoredMessage{
			Role:             "assistant",
			ReasoningContent: textContent(event.String("text")),
			ReasoningID:      event.String("reasoningId"),
			MsgID:            w.currentMsgID,
			Ts:               &ts,
		})

	case "content.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		ts := event.Timestamp
		w.appendStoredMessage(event, StoredMessage{
			Role:      "assistant",
			Content:   textContent(event.String("text")),
			ContentID: event.String("contentId"),
			MsgID:     w.currentMsgID,
			Ts:        &ts,
		})

	case "tool.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		toolID := event.String("toolId")
		toolName := event.String("toolName")
		taskID := event.String("taskId")
		ts := event.Timestamp
		w.toolNames[toolID] = toolName
		if strings.TrimSpace(taskID) != "" {
			w.toolTaskIDs[toolID] = taskID
		}
		w.appendStoredMessage(event, StoredMessage{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:   toolID,
				Type: "function",
				Function: StoredFunction{
					Name:      toolName,
					Arguments: event.String("arguments"),
				},
			}},
			ToolID: toolID,
			MsgID:  w.currentMsgID,
			Ts:     &ts,
		})

	case "tool.result":
		w.ensureStep()
		toolID := event.String("toolId")
		toolName := w.toolNames[toolID]
		if strings.TrimSpace(toolName) == "" {
			toolName = event.String("toolName")
			if strings.TrimSpace(toolName) != "" {
				w.toolNames[toolID] = toolName
			}
		}
		ts := event.Timestamp
		durationMs := durationMsPointer(event.Value("durationMs"), event.Payload)
		w.appendStoredMessage(stream.EventData{
			Seq:       event.Seq,
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"taskId": w.toolTaskIDs[toolID],
			},
		}, StoredMessage{
			Role:       "tool",
			Name:       toolName,
			ToolCallID: toolID,
			Content:    textContent(formatResult(event.Value("result"))),
			ToolID:     toolID,
			DurationMs: durationMs,
			Ts:         &ts,
		})
		w.needNewMsgID = true

	case "awaiting.ask":
		w.bufferAwaitingEvent(event)

	case "request.submit":
		w.flushCurrentStep()
		w.bufferSubmitEvent(event)

	case "awaiting.answer":
		w.writeSubmitLine(event)

	case "request.steer":
		w.flushCurrentStep()
		w.appendTypedEventLine(event, "steer")

	case "action.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		actionID := event.String("actionId")
		actionName := event.String("actionName")
		taskID := event.String("taskId")
		ts := event.Timestamp
		w.actionNames[actionID] = actionName
		if strings.TrimSpace(taskID) != "" {
			w.actionTaskIDs[actionID] = taskID
		}
		w.appendStoredMessage(event, StoredMessage{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:   actionID,
				Type: "function",
				Function: StoredFunction{
					Name:      actionName,
					Arguments: event.String("arguments"),
				},
			}},
			ActionID: actionID,
			MsgID:    w.currentMsgID,
			Ts:       &ts,
		})

	case "action.result":
		w.ensureStep()
		actionID := event.String("actionId")
		ts := event.Timestamp
		w.appendStoredMessage(stream.EventData{
			Seq:       event.Seq,
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"taskId": w.actionTaskIDs[actionID],
			},
		}, StoredMessage{
			Role:       "tool",
			Name:       w.actionNames[actionID],
			ToolCallID: actionID,
			Content:    textContent(formatResult(event.Value("result"))),
			ActionID:   actionID,
			Ts:         &ts,
		})
		w.needNewMsgID = true

	case "plan.create", "plan.update":
		w.updatePlan(event)
		w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)

	case "task.start":
		w.flushCurrentStep()
		taskID := event.String("taskId")
		if strings.TrimSpace(taskID) == "" {
			break
		}
		delete(w.closedTaskIDs, taskID)
		buffer := w.ensureTaskBuffer(taskID)
		if strings.TrimSpace(buffer.taskStage) == "" {
			buffer.taskStage = w.currentStage
		}
		buffer.taskStatus = ""
		buffer.taskSubAgentKey = event.String("subAgentKey")
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
	case "task.complete", "task.cancel", "task.error":
		taskID := event.String("taskId")
		if strings.TrimSpace(taskID) == "" {
			break
		}
		buffer := w.ensureTaskBuffer(taskID)
		switch event.Type {
		case "task.cancel":
			buffer.taskStatus = "cancelled"
		case "task.error":
			buffer.taskStatus = "failed"
		default:
			buffer.taskStatus = "completed"
		}
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		w.flushTaskStep(taskID)
		delete(w.taskBuffers, taskID)
		w.closedTaskIDs[taskID] = true

	case "artifact.publish":
		w.updateArtifact(event)
		w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)

	case "debug.llmChat":
		if inner, ok := event.Value("data").(map[string]any); ok {
			if taskID := w.taskIDForEvent(event); taskID != "" {
				if w.closedTaskIDs[taskID] {
					break
				}
				buffer := w.ensureTaskBuffer(taskID)
				w.captureTaskDebugData(buffer, inner)
				buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
			} else {
				w.captureRootDebugData(inner)
				w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
			}
		}
	case "usage.snapshot":
		if taskID := w.taskIDForEvent(event); taskID != "" {
			if w.closedTaskIDs[taskID] {
				break
			}
			buffer := w.ensureTaskBuffer(taskID)
			w.captureTaskUsageSnapshot(buffer, event)
			buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		} else {
			w.captureRootUsageSnapshot(event)
			w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
		}

	case "run.complete", "run.cancel", "run.error":
		w.flushCurrentStep()
		w.flushAllTaskSteps()
		w.flushPendingSubmit()
	}
}

// OnStageMarker processes an internal stage boundary without exposing it as a
// persisted or streamed event.
func (w *StepWriter) OnStageMarker(stage string) {
	w.flushCurrentStep()
	w.currentStage = parseStage(stage)
}

// Flush writes any remaining accumulated step. Call at end of stream.
func (w *StepWriter) Flush() {
	w.flushCurrentStep()
	w.flushAllTaskSteps()
	w.flushPendingSubmit()
}

func (w *StepWriter) RecordApproval(approval StepApproval) {
	if strings.TrimSpace(approval.Summary) == "" {
		w.pendingApproval = nil
		return
	}
	copyApproval := approval
	if len(approval.Decisions) > 0 {
		copyApproval.Decisions = append([]StepApprovalDecision(nil), approval.Decisions...)
	}
	w.pendingApproval = &copyApproval
}

// ---------------------------------------------------------------------------
// internal
// ---------------------------------------------------------------------------

func (w *StepWriter) handleRequestQuery(event stream.EventData) {
	if w.queryWritten {
		return
	}
	w.queryWritten = true

	query := map[string]any{}
	// Copy all payload fields into query, excluding seq/type/timestamp
	for key, val := range event.Payload {
		if key == "liveSeq" || key == "seq" {
			continue
		}
		query[key] = val
	}

	_ = w.store.AppendQueryLine(w.chatID, QueryLine{
		Type:      "query",
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		LiveSeq:   event.Seq,
		Query:     query,
		Systems:   append([]QueryLineSystemInit(nil), w.pendingSystemInits...),
	})
	w.pendingSystemInits = nil
}

func (w *StepWriter) ensureStep() {
	if w.currentStage == "" {
		w.currentStage = "oneshot"
	}
}

func (w *StepWriter) appendStoredMessage(event stream.EventData, message StoredMessage) {
	if taskID := w.taskIDForEvent(event); taskID != "" {
		if w.closedTaskIDs[taskID] {
			return
		}
		buffer := w.ensureTaskBuffer(taskID)
		if strings.TrimSpace(buffer.taskStage) == "" {
			buffer.taskStage = w.currentStage
		}
		buffer.messages = upsertStoredMessage(buffer.messages, message)
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		return
	}
	w.messages = upsertStoredMessage(w.messages, message)
	w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
}

func (w *StepWriter) captureRootUsageSnapshot(event stream.EventData) {
	contextWindow, _ := event.Value("contextWindow").(map[string]any)
	if usage, ok := event.Value("usage").(map[string]any); ok {
		if current, ok := usage["current"].(map[string]any); ok {
			if !hasProviderUsagePayload(current) {
				return
			}
			w.pendingUsage = usagePayloadFromSnapshotEvent(event, current, true)
			w.capturePendingModelMetadata(w.pendingUsage)
		}
	}
	if cw := contextWindow; cw != nil {
		w.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		w.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
	}
}

func (w *StepWriter) captureTaskUsageSnapshot(buffer *taskStepBuffer, event stream.EventData) {
	if buffer == nil {
		return
	}
	contextWindow, _ := event.Value("contextWindow").(map[string]any)
	if usage, ok := event.Value("usage").(map[string]any); ok {
		if current, ok := usage["current"].(map[string]any); ok {
			if !hasProviderUsagePayload(current) {
				return
			}
			buffer.pendingUsage = usagePayloadFromSnapshotEvent(event, current, true)
			buffer.capturePendingModelMetadata(buffer.pendingUsage)
		}
	}
	if cw := contextWindow; cw != nil {
		buffer.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		buffer.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
	}
}

func (w *StepWriter) captureRootDebugData(inner map[string]any) {
	model, _ := inner["model"].(map[string]any)
	if systemRef := systemRefFromDebugData(inner); len(systemRef) > 0 {
		w.pendingSystemRef = systemRef
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		w.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		w.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		w.capturePendingModelMetadata(model)
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			w.pendingUsage = usagePayloadFromMap(llm, true)
			w.capturePendingModelMetadata(w.pendingUsage, llm, model)
		}
	}
}

func (w *StepWriter) captureTaskDebugData(buffer *taskStepBuffer, inner map[string]any) {
	if buffer == nil {
		return
	}
	model, _ := inner["model"].(map[string]any)
	if systemRef := systemRefFromDebugData(inner); len(systemRef) > 0 {
		buffer.pendingSystemRef = systemRef
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		buffer.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		buffer.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		buffer.capturePendingModelMetadata(model)
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			buffer.pendingUsage = usagePayloadFromMap(llm, true)
			buffer.capturePendingModelMetadata(buffer.pendingUsage, llm, model)
		}
	}
}

func (w *StepWriter) capturePendingModelMetadata(values ...map[string]any) {
	if w == nil {
		return
	}
	captureStepModelMetadata(&w.pendingModelKey, &w.pendingReasoningEffort, values...)
}

func (w *StepWriter) flushCurrentStep() {
	w.flushCurrentStepAt(0)
}

func (w *StepWriter) flushCurrentStepAt(updatedAt int64) {
	if len(w.messages) == 0 && len(w.pendingAwaiting) == 0 {
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingEstimated = 0
		w.pendingModelKey = ""
		w.pendingReasoningEffort = ""
		w.pendingSystemRef = nil
		return
	}

	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}
	messages := append([]StoredMessage(nil), w.messages...)
	if w.pendingApproval != nil && approvalMatchesToolMessages(w.pendingApproval, messages) {
		if approvalMessage, ok := approvalAuditMessage(w.pendingApproval, updatedAt); ok {
			messages = append(messages, approvalMessage)
		}
		w.pendingApproval = nil
	}
	messages = canonicalizeStoredToolResultOrder(messages)

	line := StepLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: updatedAt,
		LiveSeq:   w.stepLiveSeq,
		Messages:  messages,
	}
	if len(w.pendingAwaiting) > 0 {
		line.Awaiting = w.pendingAwaiting
		w.pendingAwaiting = nil
	}
	if w.pendingUsage != nil {
		line.Usage = w.pendingUsage
	}
	if len(w.pendingSystemRef) > 0 {
		line.SystemRef = cloneStepSystemPayload(w.pendingSystemRef)
	}
	if w.pendingUsage != nil || w.pendingContextWindowMax > 0 || w.pendingEstimated > 0 {
		if cw := buildContextWindow(w.pendingUsage, w.pendingContextWindowMax, w.pendingEstimated); len(cw) > 0 {
			line.ContextWindow = cw
		}
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingEstimated = 0
		w.pendingSystemRef = nil
	}
	if w.latestPlan != nil {
		line.Plan = w.latestPlan
	}
	if w.latestArtifact != nil {
		line.Artifacts = w.latestArtifact
	}
	applyStepLineModelMetadata(&line, w.pendingModelKey, w.pendingReasoningEffort)

	if w.mode == "PLAN_EXECUTE" {
		line.Type = "plan-execute"
		line.Stage = w.currentStage
		// seq 只在 execute 阶段输出
		if line.Stage == "execute" {
			w.seqCounter++
			line.Seq = w.seqCounter
		}
	} else {
		w.assignReactSeq(&line)
	}

	_ = w.store.AppendStepLine(w.chatID, line)
	w.messages = nil
	w.stepLiveSeq = 0
	w.pendingUsage = nil
	w.pendingContextWindowMax = 0
	w.pendingEstimated = 0
	w.pendingModelKey = ""
	w.pendingReasoningEffort = ""
	w.pendingSystemRef = nil
}

func (w *StepWriter) assignReactSeq(line *StepLine) {
	if line == nil {
		return
	}
	if stepLineStartsModelCall(*line) {
		line.Type = StepLineTypeReact
		w.seqCounter++
		line.Seq = w.seqCounter
		return
	}
	if stepLineIsReactToolContinuation(*line) {
		line.Type = StepLineTypeReactTool
	} else {
		line.Type = StepLineTypeReact
	}
	if stepLineCanReuseReactSeq(*line) && w.seqCounter > 0 {
		line.Seq = w.seqCounter
	}
}
