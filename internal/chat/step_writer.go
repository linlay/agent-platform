package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/stream"
)

// StepWriter accumulates SSE events and writes Java-compatible JSONL lines
// (_type: "query" / "react" / "plan-execute" / "submit" / "steer" / "event")
// to the chat store.
//
// It mirrors the behaviour of Java's TurnTraceWriter:
//   - stage.marker triggers flushing the current step and starting a new one
//   - plan/artifact state is tracked and attached to step lines
//   - snapshot events (reasoning/content/tool/action) become StoredMessages
//   - request.submit + awaiting.answer are merged into SubmitLines
//   - request.steer becomes a typed EventLine so chat detail can replay it
type StepWriter struct {
	store  Store
	chatID string
	runID  string
	mode   string // "REACT" / "PLAN_EXECUTE" / "ONESHOT"
	hidden bool   // true 时跳过 QueryLine 持久化，用于系统自发触发的 run（如 schedule）

	debugEventsEnabled bool

	queryWritten bool
	seqCounter   int

	currentStage string

	messages       []StoredMessage
	latestPlan     *PlanState
	latestArtifact *ArtifactState
	taskBuffers    map[string]*taskStepBuffer
	closedTaskIDs  map[string]bool

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
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingEstimated        int
	pendingPreCallData      map[string]any
	pendingSystemRef        map[string]any
	pendingSystemInits      []QueryLineSystemInit
}

type taskStepBuffer struct {
	taskID                  string
	taskStage               string
	taskStatus              string
	taskSubAgentKey         string
	messages                []StoredMessage
	pendingPreCallData      map[string]any
	pendingSystemRef        map[string]any
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingEstimated        int
}

type StepWriterOption func(*StepWriter)

func WithDebugEventsEnabled(enabled bool) StepWriterOption {
	return func(w *StepWriter) {
		w.debugEventsEnabled = enabled
	}
}

// NewStepWriter creates a StepWriter for a single run.
// hidden=true 时跳过 QueryLine 持久化，用于 schedule 等系统自发触发的 run：
// 避免在 chat 里伪造一条"用户说的"消息、导致 webclient 显示成用户→agent 对话。
func NewStepWriter(store Store, chatID, runID, mode string, hidden bool, opts ...StepWriterOption) *StepWriter {
	w := &StepWriter{
		store:         store,
		chatID:        chatID,
		runID:         runID,
		mode:          strings.ToUpper(strings.TrimSpace(mode)),
		hidden:        hidden,
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
		// default stage; will be overridden by first stage.marker
		if w.currentStage == "" {
			w.currentStage = "oneshot"
		}

	case "stage.marker":
		w.flushCurrentStep()
		w.currentStage = parseStage(event.String("stage"))

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
		ts := event.Timestamp
		w.appendStoredMessage(stream.EventData{
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"taskId": w.toolTaskIDs[toolID],
			},
		}, StoredMessage{
			Role:       "tool",
			Name:       w.toolNames[toolID],
			ToolCallID: toolID,
			Content:    textContent(formatResult(event.Value("result"))),
			ToolID:     toolID,
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
	case "task.complete", "task.cancel", "task.fail":
		taskID := event.String("taskId")
		if strings.TrimSpace(taskID) == "" {
			break
		}
		buffer := w.ensureTaskBuffer(taskID)
		buffer.taskStatus = event.String("status")
		if buffer.taskStatus == "" {
			switch event.Type {
			case "task.cancel":
				buffer.taskStatus = "cancelled"
			case "task.fail":
				buffer.taskStatus = "failed"
			default:
				buffer.taskStatus = "completed"
			}
		}
		w.flushTaskStep(taskID)
		delete(w.taskBuffers, taskID)
		w.closedTaskIDs[taskID] = true

	case "artifact.publish":
		w.updateArtifact(event)

	case "debug.preCall", "debug.postCall":
		if inner, ok := event.Value("data").(map[string]any); ok {
			if taskID := w.taskIDForEvent(event); taskID != "" {
				if w.closedTaskIDs[taskID] {
					break
				}
				w.captureTaskDebugData(w.ensureTaskBuffer(taskID), event.Type, inner)
			} else {
				w.captureRootDebugData(event.Type, inner)
			}
		}

	case "run.complete", "run.cancel", "run.error":
		w.flushCurrentStep()
		w.flushAllTaskSteps()
		w.flushPendingSubmit()
	}
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

	// hidden run 不写 QueryLine，避免 webclient 显示成"用户→agent"对话
	if w.hidden {
		w.pendingSystemInits = nil
		return
	}

	query := map[string]any{}
	// Copy all payload fields into query, excluding seq/type/timestamp
	for key, val := range event.Payload {
		query[key] = val
	}

	_ = w.store.AppendQueryLine(w.chatID, QueryLine{
		Type:      "query",
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
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

func (w *StepWriter) ensureTaskBuffer(taskID string) *taskStepBuffer {
	buffer, ok := w.taskBuffers[taskID]
	if ok {
		return buffer
	}
	buffer = &taskStepBuffer{taskID: taskID}
	w.taskBuffers[taskID] = buffer
	return buffer
}

func (w *StepWriter) taskIDForEvent(event stream.EventData) string {
	if taskID := strings.TrimSpace(event.String("taskId")); taskID != "" {
		return taskID
	}
	if toolID := strings.TrimSpace(event.String("toolId")); toolID != "" {
		return strings.TrimSpace(w.toolTaskIDs[toolID])
	}
	if actionID := strings.TrimSpace(event.String("actionId")); actionID != "" {
		return strings.TrimSpace(w.actionTaskIDs[actionID])
	}
	return ""
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
		return
	}
	w.messages = upsertStoredMessage(w.messages, message)
}

func (w *StepWriter) captureRootDebugData(eventType string, inner map[string]any) {
	if eventType == "debug.preCall" {
		if w.debugEventsEnabled {
			w.pendingPreCallData = sanitizePreCallData(inner)
		}
		w.pendingSystemRef = systemRefFromPreCall(inner)
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		w.pendingContextWindowMax = toIntFromKeys(cw, "maxSize", "max_size")
		w.pendingEstimated = toIntFromKeys(cw, "estimatedSize", "estimated_size")
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			w.pendingUsage = usagePayloadFromMap(llm)
		}
	}
}

func (w *StepWriter) captureTaskDebugData(buffer *taskStepBuffer, eventType string, inner map[string]any) {
	if buffer == nil {
		return
	}
	if eventType == "debug.preCall" {
		if w.debugEventsEnabled {
			buffer.pendingPreCallData = sanitizePreCallData(inner)
		}
		buffer.pendingSystemRef = systemRefFromPreCall(inner)
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		buffer.pendingContextWindowMax = toIntFromKeys(cw, "maxSize", "max_size")
		buffer.pendingEstimated = toIntFromKeys(cw, "estimatedSize", "estimated_size")
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			buffer.pendingUsage = usagePayloadFromMap(llm)
		}
	}
}

func (w *StepWriter) flushCurrentStep() {
	if len(w.messages) == 0 && len(w.pendingAwaiting) == 0 {
		w.pendingApproval = nil
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingEstimated = 0
		w.pendingPreCallData = nil
		w.pendingSystemRef = nil
		return
	}

	if len(w.messages) == 0 && len(w.pendingAwaiting) > 0 {
		log.Printf("[chat] dropping pending awaiting without messages (chatId=%s runId=%s count=%d)", w.chatID, w.runID, len(w.pendingAwaiting))
		w.pendingAwaiting = nil
		w.pendingApproval = nil
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingEstimated = 0
		w.pendingPreCallData = nil
		w.pendingSystemRef = nil
		return
	}

	line := StepLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Messages:  w.messages,
	}
	if len(w.pendingAwaiting) > 0 {
		line.Awaiting = w.pendingAwaiting
		w.pendingAwaiting = nil
	}
	if w.pendingApproval != nil {
		line.Approval = w.pendingApproval
		w.pendingApproval = nil
	}
	if w.pendingUsage != nil {
		line.Usage = w.pendingUsage
	}
	if w.pendingPreCallData != nil {
		line.Debug = map[string]any{
			"preCall": cloneStepSystemPayload(w.pendingPreCallData),
		}
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
		w.pendingPreCallData = nil
		w.pendingSystemRef = nil
	}
	if w.latestPlan != nil {
		line.Plan = w.latestPlan
	}
	if w.latestArtifact != nil {
		line.Artifacts = w.latestArtifact
	}

	if w.mode == "PLAN_EXECUTE" {
		line.Type = "plan-execute"
		line.Stage = w.currentStage
		// seq 只在 execute 阶段输出
		if line.Stage == "execute" {
			w.seqCounter++
			line.Seq = w.seqCounter
		}
	} else {
		// REACT / ONESHOT 都用 react，每行都带 seq
		line.Type = "react"
		w.seqCounter++
		line.Seq = w.seqCounter
	}

	line.Messages = append([]StoredMessage(nil), w.messages...)
	_ = w.store.AppendStepLine(w.chatID, line)
	w.messages = nil
	w.pendingUsage = nil
	w.pendingContextWindowMax = 0
	w.pendingEstimated = 0
	w.pendingPreCallData = nil
	w.pendingSystemRef = nil
}

func (w *StepWriter) flushTaskStep(taskID string) {
	buffer, ok := w.taskBuffers[taskID]
	if !ok || buffer == nil {
		return
	}
	allowEmptySubAgentStep := strings.TrimSpace(buffer.taskSubAgentKey) != "" && strings.TrimSpace(buffer.taskStatus) != ""
	if len(buffer.messages) == 0 && !allowEmptySubAgentStep {
		return
	}

	line := StepLine{
		ChatID:          w.chatID,
		RunID:           w.runID,
		UpdatedAt:       time.Now().UnixMilli(),
		TaskID:          buffer.taskID,
		TaskStatus:      buffer.taskStatus,
		TaskSubAgentKey: buffer.taskSubAgentKey,
		Messages:        append([]StoredMessage(nil), buffer.messages...),
	}
	if buffer.pendingUsage != nil {
		line.Usage = buffer.pendingUsage
	}
	if buffer.pendingPreCallData != nil {
		line.Debug = map[string]any{
			"preCall": cloneStepSystemPayload(buffer.pendingPreCallData),
		}
	}
	if len(buffer.pendingSystemRef) > 0 {
		line.SystemRef = cloneStepSystemPayload(buffer.pendingSystemRef)
	}
	if buffer.pendingUsage != nil || buffer.pendingContextWindowMax > 0 || buffer.pendingEstimated > 0 {
		if cw := buildContextWindow(buffer.pendingUsage, buffer.pendingContextWindowMax, buffer.pendingEstimated); len(cw) > 0 {
			line.ContextWindow = cw
		}
	}
	if w.latestPlan != nil {
		line.Plan = w.latestPlan
	}
	if w.latestArtifact != nil {
		line.Artifacts = w.latestArtifact
	}

	if w.mode == "PLAN_EXECUTE" {
		line.Type = "plan-execute"
		line.Stage = buffer.taskStage
		if strings.TrimSpace(line.Stage) == "" {
			line.Stage = w.currentStage
		}
		if line.Stage == "execute" {
			w.seqCounter++
			line.Seq = w.seqCounter
		}
	} else {
		line.Type = "react"
		w.seqCounter++
		line.Seq = w.seqCounter
	}

	_ = w.store.AppendStepLine(w.chatID, line)
	buffer.messages = nil
	buffer.pendingUsage = nil
	buffer.pendingContextWindowMax = 0
	buffer.pendingEstimated = 0
	buffer.pendingPreCallData = nil
	buffer.pendingSystemRef = nil
}

func (w *StepWriter) flushAllTaskSteps() {
	taskIDs := make([]string, 0, len(w.taskBuffers))
	for taskID := range w.taskBuffers {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		w.flushTaskStep(taskID)
		delete(w.taskBuffers, taskID)
	}
}

func (w *StepWriter) bufferAwaitingEvent(event stream.EventData) {
	m := event.Map()
	delete(m, "seq")
	w.pendingAwaiting = append(w.pendingAwaiting, m)
}

func (w *StepWriter) bufferSubmitEvent(event stream.EventData) {
	if w.pendingSubmit != nil {
		w.flushPendingSubmit()
	}
	w.pendingSubmit = event.Map()
}

func (w *StepWriter) flushPendingSubmit() {
	if w.store == nil || w.pendingSubmit == nil {
		return
	}
	_ = w.store.AppendSubmitLine(w.chatID, SubmitLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Submit:    w.pendingSubmit,
		Type:      "submit",
	})
	w.pendingSubmit = nil
}

func (w *StepWriter) writeSubmitLine(answer stream.EventData) {
	if w.store == nil {
		w.pendingSubmit = nil
		return
	}
	answerPayload := answer.Map()
	delete(answerPayload, "seq")
	_ = w.store.AppendSubmitLine(w.chatID, SubmitLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Submit:    w.pendingSubmit,
		Answer:    answerPayload,
		Type:      "submit",
	})
	w.pendingSubmit = nil
}

func (w *StepWriter) appendTypedEventLine(event stream.EventData, lineType string) {
	if w.store == nil {
		return
	}
	_ = w.store.AppendEventLine(w.chatID, EventLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Event:     event.Map(),
		Type:      lineType,
	})
}

func (w *StepWriter) updatePlan(event stream.EventData) {
	planID := event.String("planId")
	plan := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}

	// The "plan" field in plan.update is the tasks array directly.
	// Runtime type may be []map[string]any (from Go engine) or []any (from JSON).
	rawPlan := event.Value("plan")
	for _, mapped := range toMapSlice(rawPlan) {
		plan.Tasks = append(plan.Tasks, PlanTaskState{
			TaskID:      stringVal(mapped["taskId"]),
			Description: stringVal(mapped["description"]),
			Status:      stringVal(mapped["status"]),
		})
	}
	w.latestPlan = plan
}

func (w *StepWriter) updateArtifact(event stream.EventData) {
	if w.latestArtifact == nil {
		w.latestArtifact = &ArtifactState{}
	}
	w.latestArtifact.Items = append(w.latestArtifact.Items, artifactItemsFromEventPayload(event.Payload)...)
}

func (w *StepWriter) ensureMsgID() {
	if w.currentMsgID == "" || w.needNewMsgID {
		w.currentMsgID = generateMsgID()
		w.needNewMsgID = false
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func textContent(text string) []ContentPart {
	return []ContentPart{{Type: "text", Text: text}}
}

func formatResult(v any) string {
	if v == nil {
		return ""
	}
	if text, ok := v.(string); ok {
		return text
	}
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprintf("%v", v)
}

func upsertStoredMessage(messages []StoredMessage, message StoredMessage) []StoredMessage {
	key := storedMessageUpsertKey(message)
	if key == "" {
		return append(messages, message)
	}
	for index := range messages {
		if storedMessageUpsertKey(messages[index]) == key {
			messages[index] = mergeStoredMessageSnapshot(messages[index], message)
			return messages
		}
	}
	return append(messages, message)
}

func storedMessageUpsertKey(message StoredMessage) string {
	if id := strings.TrimSpace(message.ContentID); id != "" {
		return "content:" + id
	}
	if id := strings.TrimSpace(message.ReasoningID); id != "" {
		return "reasoning:" + id
	}
	if id := strings.TrimSpace(message.ToolID); id != "" {
		return strings.TrimSpace(message.Role) + ":tool:" + id
	}
	if id := strings.TrimSpace(message.ActionID); id != "" {
		return strings.TrimSpace(message.Role) + ":action:" + id
	}
	if id := strings.TrimSpace(message.ToolCallID); id != "" {
		return strings.TrimSpace(message.Role) + ":tool-call:" + id
	}
	return ""
}

func mergeStoredMessageSnapshot(existing StoredMessage, incoming StoredMessage) StoredMessage {
	if storedMessageTextLen(incoming) < storedMessageTextLen(existing) {
		return existing
	}
	return incoming
}

func storedMessageTextLen(message StoredMessage) int {
	total := 0
	for _, part := range message.Content {
		total += len(part.Text)
	}
	for _, part := range message.ReasoningContent {
		total += len(part.Text)
	}
	for _, call := range message.ToolCalls {
		total += len(call.Function.Arguments)
	}
	return total
}

func stringVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// toMapSlice converts an any value to []map[string]any.
// Handles both []any (from JSON unmarshal) and []map[string]any (from Go engine).
func toMapSlice(v any) []map[string]any {
	switch typed := v.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	default:
		return nil
	}
}

func generateMsgID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}

func cloneStepSystemPayload(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return cloneStringAnyMap(value)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return cloneStringAnyMap(value)
	}
	return cloned
}

func buildContextWindow(usage map[string]any, maxSize int, estimatedSize int) map[string]any {
	actual := 0
	if usage != nil {
		actual = toIntFromKeys(usage, "promptTokens", "prompt_tokens")
	}
	cw := map[string]any{}
	if maxSize > 0 {
		cw["maxSize"] = maxSize
	}
	if actual > 0 {
		cw["actualSize"] = actual
	}
	if estimatedSize > 0 {
		cw["estimatedSize"] = estimatedSize
	}
	if len(cw) == 0 {
		return nil
	}
	return cw
}

func sanitizePreCallData(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := cloneStepSystemPayload(value)
	delete(out, "usage")
	delete(out, "contextWindow")
	if requestBody, ok := out["requestBody"].(map[string]any); ok {
		out["requestBody"] = sanitizeRequestBodyForStep(requestBody)
	}
	return out
}

func sanitizeRequestBodyForStep(requestBody map[string]any) map[string]any {
	if len(requestBody) == 0 {
		return nil
	}
	out := cloneStepSystemPayload(requestBody)
	delete(out, "messages")
	delete(out, "system")
	delete(out, "tools")
	return out
}

func systemRefFromPreCall(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	raw, _ := value["systemRef"].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	return cloneStepSystemPayload(raw)
}

// parseStage normalises a stage marker string to a stage name, matching Java's
// TurnTraceWriter.parseStage behaviour.
func parseStage(marker string) string {
	marker = strings.TrimSpace(marker)
	switch {
	case strings.HasPrefix(marker, "react-step"):
		return "react"
	case marker == "plan-draft" || marker == "plan-generate":
		return "plan"
	case strings.HasPrefix(marker, "execute-task"):
		return "execute"
	case marker == "summary":
		return "summary"
	default:
		return marker
	}
}
