package chat

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/stream"
)

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
		w.assignReactSeq(&line)
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

func (w *StepWriter) handlePlanningEvent(event stream.EventData) {
	w.updatePlanning(event)
	switch event.Type {
	case "planning.start", "planning.delta", "planning.end":
		if w.debugEventsEnabled {
			w.appendTypedEventLine(event, "planning")
		}
		if event.Type == "planning.end" {
			w.appendTypedEventLine(w.planningSnapshotEvent(event), "planning")
		}
	case "planning.snapshot":
		w.appendTypedEventLine(event, "planning")
	}
}

func (w *StepWriter) planningSnapshotEvent(source stream.EventData) stream.EventData {
	payload := map[string]any{}
	if w.latestPlanning != nil {
		payload["planningId"] = w.latestPlanning.PlanningID
		payload["planningFile"] = planningFileDisplayName(w.latestPlanning.PlanningFile)
		payload["title"] = w.latestPlanning.Title
		payload["markdown"] = w.latestPlanning.Markdown
		payload["updatedAt"] = w.latestPlanning.UpdatedAt
	}
	if value := strings.TrimSpace(source.String("chatId")); value != "" {
		payload["chatId"] = value
	} else {
		payload["chatId"] = w.chatID
	}
	if value := strings.TrimSpace(source.String("runId")); value != "" {
		payload["runId"] = value
	} else {
		payload["runId"] = w.runID
	}
	if payload["updatedAt"] == nil || int64FromAny(payload["updatedAt"]) == 0 {
		payload["updatedAt"] = source.Timestamp
	}
	return stream.EventData{
		Type:      "planning.snapshot",
		Timestamp: source.Timestamp,
		Payload:   payload,
	}
}

func planningFileDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Base(value)
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

func (w *StepWriter) updatePlanning(event stream.EventData) {
	if event.Type == "planning.start" {
		if planningID := strings.TrimSpace(event.String("planningId")); planningID != "" &&
			w.latestPlanning != nil &&
			w.latestPlanning.PlanningID != "" &&
			w.latestPlanning.PlanningID != planningID {
			w.latestPlanning = nil
		}
	}
	if w.latestPlanning == nil {
		w.latestPlanning = &PlanningState{}
	}
	if value := strings.TrimSpace(event.String("planningId")); value != "" {
		w.latestPlanning.PlanningID = value
	}
	if value := strings.TrimSpace(event.String("planningFile")); value != "" {
		w.latestPlanning.PlanningFile = value
	}
	if value := strings.TrimSpace(event.String("title")); value != "" {
		w.latestPlanning.Title = value
	}
	if value := strings.TrimSpace(event.String("status")); value != "" {
		w.latestPlanning.Status = value
	}
	if value := event.String("delta"); value != "" {
		w.latestPlanning.Markdown += value
	}
	if value := strings.TrimSpace(event.String("markdown")); value != "" {
		w.latestPlanning.Markdown = value
	}
	if updatedAt := event.Value("updatedAt"); updatedAt != nil {
		w.latestPlanning.UpdatedAt = int64FromAny(updatedAt)
	}
	if w.latestPlanning.UpdatedAt == 0 {
		w.latestPlanning.UpdatedAt = event.Timestamp
	}
}

func (w *StepWriter) updateArtifact(event stream.EventData) {
	if w.latestArtifact == nil {
		w.latestArtifact = &ArtifactState{}
	}
	w.latestArtifact.Items = append(w.latestArtifact.Items, artifactItemsFromEventPayload(event.Payload)...)
}
