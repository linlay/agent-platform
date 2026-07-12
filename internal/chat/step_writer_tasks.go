package chat

import (
	"sort"
	"strings"

	"agent-platform/internal/stream"
)

type taskStepBuffer struct {
	taskID                  string
	taskStage               string
	taskStatus              string
	taskSubAgentKey         string
	teamID                  string
	presentation            string
	messages                []StoredMessage
	sources                 *SourceState
	liveSeq                 int64
	lastTimestamp           int64
	pendingSystemRef        map[string]any
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingContextCurrent   int
	pendingEstimated        int
	pendingModelKey         string
	pendingReasoningEffort  string
	pendingInputMessages    []map[string]any
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
	if len(buffer.messages) == 0 && !allowEmptySubAgentStep && (buffer.sources == nil || len(buffer.sources.Items) == 0) {
		return
	}

	line := StepLine{
		ChatID:          w.chatID,
		RunID:           w.runID,
		UpdatedAt:       buffer.lastTimestamp,
		LiveSeq:         buffer.liveSeq,
		TaskID:          buffer.taskID,
		TaskStatus:      buffer.taskStatus,
		TaskSubAgentKey: buffer.taskSubAgentKey,
		TeamID:          buffer.teamID,
		Presentation:    buffer.presentation,
		Messages:        canonicalizeStoredToolResultOrder(append([]StoredMessage(nil), buffer.messages...)),
	}
	if buffer.pendingUsage != nil {
		line.Usage = buffer.pendingUsage
	}
	if len(buffer.pendingSystemRef) > 0 {
		line.SystemRef = cloneStepSystemPayload(buffer.pendingSystemRef)
	}
	if len(buffer.pendingInputMessages) > 0 {
		line.InputMessages = cloneMessageMaps(buffer.pendingInputMessages)
	}
	if buffer.pendingUsage != nil || buffer.pendingContextWindowMax > 0 || buffer.pendingContextCurrent > 0 || buffer.pendingEstimated > 0 {
		if cw := buildContextWindow(buffer.pendingContextWindowMax, buffer.pendingContextCurrent, buffer.pendingEstimated); len(cw) > 0 {
			line.ContextWindow = cw
		}
	}
	if w.latestArtifact != nil {
		line.Artifacts = w.latestArtifact
	}
	if buffer.sources != nil {
		line.Sources = cloneSourceState(buffer.sources)
	}
	applyStepLineModelMetadata(&line, buffer.pendingModelKey, buffer.pendingReasoningEffort)

	if w.mode == "PLAN_EXECUTE" {
		line.Stage = buffer.taskStage
		if strings.TrimSpace(line.Stage) == "" {
			line.Stage = w.currentStage
		}
	}
	w.assignReactSeq(&line)

	if line.UpdatedAt > 0 {
		if err := w.store.AppendStepLine(w.chatID, line); err != nil {
			w.recordPersistenceError(err)
			return
		}
	}
	buffer.messages = nil
	buffer.sources = nil
	buffer.liveSeq = 0
	buffer.pendingUsage = nil
	buffer.pendingContextWindowMax = 0
	buffer.pendingContextCurrent = 0
	buffer.pendingEstimated = 0
	buffer.pendingModelKey = ""
	buffer.pendingReasoningEffort = ""
	buffer.pendingInputMessages = nil
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
	w.pendingSubmitTimestamp = event.Timestamp
}

func (w *StepWriter) flushPendingSubmit() {
	if w.store == nil || w.pendingSubmit == nil {
		return
	}
	if w.pendingSubmitTimestamp > 0 {
		if err := w.store.AppendSubmitLine(w.chatID, SubmitLine{
			ChatID:    w.chatID,
			RunID:     w.runID,
			UpdatedAt: w.pendingSubmitTimestamp,
			LiveSeq:   w.pendingSubmitLiveSeq,
			Submit:    w.pendingSubmit,
			Type:      "submit",
		}); err != nil {
			w.recordPersistenceError(err)
			return
		}
	}
	w.pendingSubmit = nil
	w.pendingSubmitLiveSeq = 0
	w.pendingSubmitTimestamp = 0
}

func (w *StepWriter) writeSubmitLine(answer stream.EventData) {
	if w.store == nil {
		w.pendingSubmit = nil
		w.pendingSubmitLiveSeq = 0
		w.pendingSubmitTimestamp = 0
		return
	}
	answerPayload := eventPayloadWithoutSeq(answer)
	if answer.Timestamp > 0 {
		if err := w.store.AppendSubmitLine(w.chatID, SubmitLine{
			ChatID:    w.chatID,
			RunID:     w.runID,
			UpdatedAt: answer.Timestamp,
			LiveSeq:   maxLiveSeq(w.pendingSubmitLiveSeq, answer.Seq),
			Submit:    w.pendingSubmit,
			Answer:    answerPayload,
			Type:      "submit",
		}); err != nil {
			w.recordPersistenceError(err)
			return
		}
	}
	w.pendingSubmit = nil
	w.pendingSubmitLiveSeq = 0
	w.pendingSubmitTimestamp = 0
}

func (w *StepWriter) appendTypedEventLine(event stream.EventData, lineType string) {
	if w.store == nil {
		return
	}
	if event.Timestamp <= 0 {
		return
	}
	if err := w.store.AppendEventLine(w.chatID, EventLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: event.Timestamp,
		LiveSeq:   event.Seq,
		Event:     eventPayloadWithoutSeq(event),
		Type:      lineType,
	}); err != nil {
		w.recordPersistenceError(err)
	}
}

func (w *StepWriter) updateArtifact(event stream.EventData) {
	if w.latestArtifact == nil {
		w.latestArtifact = &ArtifactState{}
	}
	w.latestArtifact.Items = append(w.latestArtifact.Items, artifactItemsFromEventPayload(event.Payload)...)
}
