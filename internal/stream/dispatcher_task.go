package stream

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

func (d *StreamEventDispatcher) handlePlanUpdate(input PlanUpdate) []StreamEvent {
	eventType := "plan.update"
	d.state.planID = input.PlanID
	return []StreamEvent{NewEvent(eventType, map[string]any{
		"planId": input.PlanID,
		"plan":   input.Plan,
		"chatId": input.ChatID,
	})}
}

func (d *StreamEventDispatcher) handleTaskStart(input TaskStart) []StreamEvent {
	d.state.activeTaskID = input.TaskID
	payload := map[string]any{
		"taskId":      input.TaskID,
		"runId":       input.RunID,
		"groupId":     input.GroupID,
		"taskName":    input.TaskName,
		"description": input.Description,
		"subAgentKey": input.SubAgentKey,
		"mainToolId":  input.MainToolID,
	}
	return []StreamEvent{NewEvent("task.start", payload)}
}

func (d *StreamEventDispatcher) handleTaskComplete(input TaskComplete) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.complete", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
	})}
}

func (d *StreamEventDispatcher) handleTaskCancel(input TaskCancel) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.cancel", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
	})}
}

func (d *StreamEventDispatcher) handleTaskFail(input TaskFail) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.fail", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
		"error":  normalizeErrorMap(input.Error, "task_failed", "task", "runtime"),
	})}
}

func (d *StreamEventDispatcher) handleSourcePublish(input SourcePublish) []StreamEvent {
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = d.request.RunID
	}

	sources, chunkCount := normalizeSources(input.Sources)
	payload := map[string]any{
		"publishId":   sourcePublishID(input.PublishID),
		"runId":       runID,
		"kind":        input.Kind,
		"sourceCount": len(sources),
		"chunkCount":  chunkCount,
		"sources":     sources,
	}
	if taskID := strings.TrimSpace(input.TaskID); taskID != "" {
		payload["taskId"] = taskID
	}
	if toolID := strings.TrimSpace(input.ToolID); toolID != "" {
		payload["toolId"] = toolID
	}
	if query := strings.TrimSpace(input.Query); query != "" {
		payload["query"] = query
	}

	return []StreamEvent{NewEvent("source.publish", payload)}
}

func normalizeSources(input []Source) ([]Source, int) {
	if len(input) == 0 {
		return []Source{}, 0
	}

	sources := make([]Source, 0, len(input))
	chunkCount := 0
	for _, source := range input {
		chunks := make([]SourceChunk, len(source.Chunks))
		copy(chunks, source.Chunks)
		sort.SliceStable(chunks, func(i, j int) bool {
			return chunks[i].Index < chunks[j].Index
		})

		chunkIndexes := make([]int, 0, len(chunks))
		minIndex := 0
		if len(chunks) > 0 {
			minIndex = chunks[0].Index
		}
		for _, chunk := range chunks {
			chunkIndexes = append(chunkIndexes, chunk.Index)
		}

		sources = append(sources, Source{
			ID:             source.ID,
			Name:           source.Name,
			Title:          source.Title,
			Icon:           source.Icon,
			URL:            source.URL,
			Link:           source.Link,
			CollectionID:   source.CollectionID,
			CollectionName: source.CollectionName,
			ChunkIndexes:   chunkIndexes,
			MinIndex:       minIndex,
			Chunks:         chunks,
		})
		chunkCount += len(chunks)
	}

	return sources, chunkCount
}

func sourcePublishID(input string) string {
	if id := strings.TrimSpace(input); id != "" {
		return id
	}
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "src-" + hex.EncodeToString(buf)
	}
	return "src-" + strconvBase16(time.Now().UnixNano())
}

func strconvBase16(value int64) string {
	const digits = "0123456789abcdef"
	if value == 0 {
		return "0"
	}
	if value < 0 {
		value = -value
	}
	out := make([]byte, 0, 16)
	for value > 0 {
		out = append(out, digits[value%16])
		value /= 16
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
