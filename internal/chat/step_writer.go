package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
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

	queryWritten bool
	seqCounter   int

	currentStage           string
	currentTaskID          string
	currentTaskName        string
	currentTaskDescription string
	currentTaskStatus      string
	currentTaskSubAgentKey string
	currentTaskMainToolID  string
	currentTaskIsSubAgent  bool

	messages       []StoredMessage
	latestPlan     *PlanState
	latestArtifact *ArtifactState

	// tool/action name tracking (for tool.result → StoredMessage.Name)
	toolNames   map[string]string
	actionNames map[string]string

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
}

// NewStepWriter creates a StepWriter for a single run.
func NewStepWriter(store Store, chatID, runID, mode string) *StepWriter {
	return &StepWriter{
		store:       store,
		chatID:      chatID,
		runID:       runID,
		mode:        strings.ToUpper(strings.TrimSpace(mode)),
		toolNames:   map[string]string{},
		actionNames: map[string]string{},
	}
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
		w.messages = append(w.messages, StoredMessage{
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
		w.messages = append(w.messages, StoredMessage{
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
		ts := event.Timestamp
		w.toolNames[toolID] = toolName
		w.messages = append(w.messages, StoredMessage{
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
		w.messages = append(w.messages, StoredMessage{
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
		w.flushCurrentStep()
		w.writeSubmitLine(event)

	case "request.steer":
		w.flushCurrentStep()
		w.appendTypedEventLine(event, "steer")

	case "action.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		actionID := event.String("actionId")
		actionName := event.String("actionName")
		ts := event.Timestamp
		w.actionNames[actionID] = actionName
		w.messages = append(w.messages, StoredMessage{
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
		w.messages = append(w.messages, StoredMessage{
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
		w.currentTaskID = event.String("taskId")
		w.currentTaskName = event.String("taskName")
		w.currentTaskDescription = event.String("description")
		w.currentTaskStatus = ""
		w.currentTaskSubAgentKey = event.String("subAgentKey")
		w.currentTaskMainToolID = event.String("mainToolId")
		w.currentTaskIsSubAgent = strings.TrimSpace(w.currentTaskSubAgentKey) != ""
	case "task.complete", "task.cancel", "task.fail":
		w.currentTaskStatus = event.String("status")
		if w.currentTaskStatus == "" {
			switch event.Type {
			case "task.cancel":
				w.currentTaskStatus = "cancelled"
			case "task.fail":
				w.currentTaskStatus = "error"
			default:
				w.currentTaskStatus = "completed"
			}
		}
		w.flushCurrentStep()
		w.currentTaskID = ""
		w.currentTaskName = ""
		w.currentTaskDescription = ""
		w.currentTaskStatus = ""
		w.currentTaskSubAgentKey = ""
		w.currentTaskMainToolID = ""
		w.currentTaskIsSubAgent = false

	case "artifact.publish":
		w.updateArtifact(event)

	case "debug.preCall", "debug.postCall":
		if inner, ok := event.Value("data").(map[string]any); ok {
			if cw, ok := inner["contextWindow"].(map[string]any); ok {
				w.pendingContextWindowMax = toInt(cw["max_size"])
				w.pendingEstimated = toInt(cw["estimated_size"])
			}
			if usage, ok := inner["usage"].(map[string]any); ok {
				if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
					w.pendingUsage = map[string]any{
						"prompt_tokens":     toInt(llm["promptTokens"]),
						"completion_tokens": toInt(llm["completionTokens"]),
						"total_tokens":      toInt(llm["totalTokens"]),
					}
				}
			}
		}

	case "run.complete", "run.cancel", "run.error":
		w.flushCurrentStep()
		w.flushPendingSubmit()
	}
}

// Flush writes any remaining accumulated step. Call at end of stream.
func (w *StepWriter) Flush() {
	w.flushCurrentStep()
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
		query[key] = val
	}

	_ = w.store.AppendQueryLine(w.chatID, QueryLine{
		Type:      "query",
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Query:     query,
	})
}

func (w *StepWriter) ensureStep() {
	if w.currentStage == "" {
		w.currentStage = "oneshot"
	}
}

func (w *StepWriter) flushCurrentStep() {
	allowEmptySubAgentStep := w.currentTaskIsSubAgent && strings.TrimSpace(w.currentTaskID) != "" && strings.TrimSpace(w.currentTaskStatus) != ""
	if len(w.messages) == 0 && len(w.pendingAwaiting) == 0 && !allowEmptySubAgentStep {
		w.pendingApproval = nil
		return
	}

	if len(w.messages) == 0 && len(w.pendingAwaiting) > 0 && !allowEmptySubAgentStep {
		log.Printf("[chat] dropping pending awaiting without messages (chatId=%s runId=%s count=%d)", w.chatID, w.runID, len(w.pendingAwaiting))
		w.pendingAwaiting = nil
		w.pendingApproval = nil
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
	if w.pendingUsage != nil || w.pendingContextWindowMax > 0 || w.pendingEstimated > 0 {
		actual := 0
		if w.pendingUsage != nil {
			actual = toInt(w.pendingUsage["prompt_tokens"])
		}
		cw := map[string]any{}
		if w.pendingContextWindowMax > 0 {
			cw["max_size"] = w.pendingContextWindowMax
		}
		if actual > 0 {
			cw["actual_size"] = actual
		}
		if w.pendingEstimated > 0 {
			cw["estimated_size"] = w.pendingEstimated
		}
		if len(cw) > 0 {
			line.ContextWindow = cw
		}
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingEstimated = 0
	}
	if w.currentTaskID != "" {
		line.TaskID = w.currentTaskID
		line.TaskName = w.currentTaskName
		line.TaskDescription = w.currentTaskDescription
		line.TaskStatus = w.currentTaskStatus
		line.TaskSubAgentKey = w.currentTaskSubAgentKey
		line.TaskMainToolID = w.currentTaskMainToolID
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
		if w.currentStage == "execute" {
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
	item, _ := event.Value("artifact").(map[string]any)
	if item == nil {
		return
	}
	w.latestArtifact.Items = append(w.latestArtifact.Items, ArtifactItemState{
		ArtifactID: stringVal(event.Value("artifactId")),
		Type:       stringVal(item["type"]),
		Name:       stringVal(item["name"]),
		MimeType:   stringVal(item["mimeType"]),
		URL:        stringVal(item["url"]),
		SHA256:     stringVal(item["sha256"]),
	})
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
