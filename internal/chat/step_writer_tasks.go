package chat

import (
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
	liveSeq                 int64
	pendingSystemRef        map[string]any
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingContextCurrent   int
	pendingEstimated        int
	pendingModelKey         string
	pendingReasoningEffort  string
	pendingInputMessages    []map[string]any
	pendingStepSystems      []QueryLineSystemInit
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

func (buffer *taskStepBuffer) capturePendingModelMetadata(values ...map[string]any) {
	if buffer == nil {
		return
	}
	captureStepModelMetadata(&buffer.pendingModelKey, &buffer.pendingReasoningEffort, values...)
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
		LiveSeq:         buffer.liveSeq,
		TaskID:          buffer.taskID,
		TaskStatus:      buffer.taskStatus,
		TaskSubAgentKey: buffer.taskSubAgentKey,
		Messages:        canonicalizeStoredToolResultOrder(append([]StoredMessage(nil), buffer.messages...)),
	}
	if buffer.pendingUsage != nil {
		line.Usage = buffer.pendingUsage
	}
	if len(buffer.pendingSystemRef) > 0 {
		line.SystemRef = cloneStepSystemPayload(buffer.pendingSystemRef)
	}
	if len(buffer.pendingStepSystems) > 0 {
		line.Systems = cloneQueryLineSystemInits(buffer.pendingStepSystems)
		for _, profile := range buffer.pendingStepSystems {
			w.markKnownSystemProfile(profile)
		}
	}
	if len(buffer.pendingInputMessages) > 0 {
		line.InputMessages = cloneMessageMaps(buffer.pendingInputMessages)
	}
	if buffer.pendingUsage != nil || buffer.pendingContextWindowMax > 0 || buffer.pendingContextCurrent > 0 || buffer.pendingEstimated > 0 {
		if cw := buildContextWindow(buffer.pendingContextWindowMax, buffer.pendingContextCurrent, buffer.pendingEstimated); len(cw) > 0 {
			line.ContextWindow = cw
		}
	}
	if w.latestPlan != nil {
		line.Plan = w.latestPlan
	}
	if w.latestArtifact != nil {
		line.Artifacts = w.latestArtifact
	}
	applyStepLineModelMetadata(&line, buffer.pendingModelKey, buffer.pendingReasoningEffort)

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
	buffer.liveSeq = 0
	buffer.pendingUsage = nil
	buffer.pendingContextWindowMax = 0
	buffer.pendingContextCurrent = 0
	buffer.pendingEstimated = 0
	buffer.pendingModelKey = ""
	buffer.pendingReasoningEffort = ""
	buffer.pendingInputMessages = nil
	buffer.pendingSystemRef = nil
	buffer.pendingStepSystems = nil
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
	m := eventPayloadWithoutSeq(event)
	w.pendingAwaiting = append(w.pendingAwaiting, m)
	w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
	w.flushCurrentStepAt(event.Timestamp)
}

func (w *StepWriter) bufferSubmitEvent(event stream.EventData) {
	if w.pendingSubmit != nil {
		w.flushPendingSubmit()
	}
	w.pendingSubmit = eventPayloadWithoutSeq(event)
	w.pendingSubmitLiveSeq = event.Seq
}

func (w *StepWriter) flushPendingSubmit() {
	if w.store == nil || w.pendingSubmit == nil {
		return
	}
	_ = w.store.AppendSubmitLine(w.chatID, SubmitLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		LiveSeq:   w.pendingSubmitLiveSeq,
		Submit:    w.pendingSubmit,
		Type:      "submit",
	})
	w.pendingSubmit = nil
	w.pendingSubmitLiveSeq = 0
}

func (w *StepWriter) writeSubmitLine(answer stream.EventData) {
	if w.store == nil {
		w.pendingSubmit = nil
		w.pendingSubmitLiveSeq = 0
		return
	}
	answerPayload := eventPayloadWithoutSeq(answer)
	_ = w.store.AppendSubmitLine(w.chatID, SubmitLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		LiveSeq:   maxLiveSeq(w.pendingSubmitLiveSeq, answer.Seq),
		Submit:    w.pendingSubmit,
		Answer:    answerPayload,
		Type:      "submit",
	})
	w.pendingSubmit = nil
	w.pendingSubmitLiveSeq = 0
}

func (w *StepWriter) appendTypedEventLine(event stream.EventData, lineType string) {
	if w.store == nil {
		return
	}
	_ = w.store.AppendEventLine(w.chatID, EventLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: time.Now().UnixMilli(),
		LiveSeq:   event.Seq,
		Event:     eventPayloadWithoutSeq(event),
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
