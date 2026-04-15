package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"agent-platform-runner-go/internal/stream"
)

// StepWriter accumulates SSE events and writes Java-compatible JSONL lines
// (_type: "query" / "step" / "event") to the chat store.
//
// It mirrors the behaviour of Java's TurnTraceWriter:
//   - stage.marker triggers flushing the current step and starting a new one
//   - plan/artifact state is tracked and attached to step lines
//   - snapshot events (reasoning/content/tool/action) become StoredMessages
//   - confirm/request lifecycle events become EventLines so chat detail can replay them
type StepWriter struct {
	store  Store
	chatID string
	runID  string
	mode   string // "REACT" / "PLAN_EXECUTE" / "ONESHOT"

	queryWritten bool
	seqCounter   int

	currentStage  string
	currentTaskID string

	messages       []StoredMessage
	latestPlan     *PlanState
	latestArtifact *ArtifactState

	// tool/action name tracking (for tool.result → StoredMessage.Name)
	toolNames   map[string]string
	actionNames map[string]string

	// msgId generation
	currentMsgID string
	needNewMsgID bool
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

	case "awaiting.ask", "awaiting.payload", "request.submit", "request.steer":
		w.flushCurrentStep()
		w.appendEventLine(event)

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
		w.currentTaskID = event.String("taskId")
	case "task.complete", "task.cancel", "task.fail":
		w.currentTaskID = ""

	case "artifact.publish":
		w.updateArtifact(event)

	case "run.complete", "run.cancel", "run.error":
		w.flushCurrentStep()
	}
}

// Flush writes any remaining accumulated step. Call at end of stream.
func (w *StepWriter) Flush() {
	w.flushCurrentStep()
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
	if len(w.messages) == 0 {
		return
	}

	line := StepLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Messages:  w.messages,
	}
	if w.currentTaskID != "" {
		line.TaskID = w.currentTaskID
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

	_ = w.store.AppendStepLine(w.chatID, line)
	w.messages = nil
}

func (w *StepWriter) appendEventLine(event stream.EventData) {
	if w.store == nil {
		return
	}
	_ = w.store.AppendEventLine(w.chatID, EventLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		Event:     event.Map(),
		Type:      "event",
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
	return fmt.Sprintf("%v", v)
}

func stringVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
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
