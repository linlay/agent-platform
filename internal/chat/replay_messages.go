package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

func storedMessageToEvents(msg map[string]any, runID, taskID, stage string, liveSeq int64, nextSeq func() int64) []stream.EventData {
	role, _ := msg["role"].(string)
	ts := int64FromAny(msg["ts"])
	var events []stream.EventData

	switch role {
	case "assistant":
		if rc, ok := msg["reasoning_content"]; ok {
			text := extractTextFromContent(rc)
			if text != "" {
				reasoningID, _ := msg["_reasoningId"].(string)
				payload := map[string]any{
					"reasoningId":    reasoningID,
					"runId":          runID,
					"text":           text,
					"taskId":         taskID,
					"reasoningLabel": stream.ReasoningLabelForID(reasoningID),
				}
				addReplayLiveSeq(payload, liveSeq)
				events = append(events, stream.EventData{
					Seq:       nextSeq(),
					Type:      "reasoning.snapshot",
					Timestamp: ts,
					Payload:   payload,
				})
			}
		}
		if c, ok := msg["content"]; ok {
			text := extractTextFromContent(c)
			if text != "" {
				contentID, _ := msg["_contentId"].(string)
				payload := map[string]any{
					"contentId": contentID,
					"runId":     runID,
					"text":      text,
					"taskId":    taskID,
				}
				addReplayLiveSeq(payload, liveSeq)
				events = append(events, stream.EventData{
					Seq:       nextSeq(),
					Type:      "content.snapshot",
					Timestamp: ts,
					Payload:   payload,
				})
			}
		}
		if tcs, ok := msg["tool_calls"].([]any); ok {
			actionID, _ := msg["_actionId"].(string)
			toolID, _ := msg["_toolId"].(string)
			for _, tc := range tcs {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				fn, _ := tcMap["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				callID, _ := tcMap["id"].(string)
				fnName, _ := fn["name"].(string)
				fnArgs, _ := fn["arguments"].(string)

				if actionID != "" {
					payload := map[string]any{
						"actionId":   callID,
						"runId":      runID,
						"actionName": fnName,
						"taskId":     taskID,
						"arguments":  fnArgs,
					}
					addReplayLiveSeq(payload, liveSeq)
					events = append(events, stream.EventData{
						Seq:       nextSeq(),
						Type:      "action.snapshot",
						Timestamp: ts,
						Payload:   payload,
					})
				} else {
					id := toolID
					if id == "" {
						id = callID
					}
					payload := map[string]any{
						"toolId":    id,
						"runId":     runID,
						"toolName":  fnName,
						"taskId":    taskID,
						"arguments": fnArgs,
					}
					addReplayLiveSeq(payload, liveSeq)
					events = append(events, stream.EventData{
						Seq:       nextSeq(),
						Type:      "tool.snapshot",
						Timestamp: ts,
						Payload:   payload,
					})
				}
			}
		}

	case "tool":
		text := extractTextFromContent(msg["content"])
		actionID, _ := msg["_actionId"].(string)
		toolID, _ := msg["_toolId"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)

		if actionID != "" {
			payload := map[string]any{
				"actionId": toolCallID,
				"result":   text,
			}
			addReplayLiveSeq(payload, liveSeq)
			events = append(events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "action.result",
				Timestamp: ts,
				Payload:   payload,
			})
		} else {
			id := toolID
			if id == "" {
				id = toolCallID
			}
			payload := map[string]any{
				"toolId": id,
				"result": text,
			}
			if _, ok := msg["durationMs"]; ok {
				payload["durationMs"] = msg["durationMs"]
			}
			addReplayLiveSeq(payload, liveSeq)
			events = append(events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "tool.result",
				Timestamp: ts,
				Payload:   payload,
			})
		}
	}

	return events
}

func extractTextFromContent(v any) string {
	if parts, ok := v.([]any); ok {
		var sb strings.Builder
		for _, part := range parts {
			if pMap, ok := part.(map[string]any); ok {
				if text, ok := pMap["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	if text, ok := v.(string); ok {
		return text
	}
	return ""
}

func parsePlanFromStep(raw map[string]any) *PlanState {
	planID, _ := raw["planId"].(string)
	plan := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}
	tasks, _ := raw["tasks"].([]any)
	for _, t := range tasks {
		tMap, _ := t.(map[string]any)
		if tMap == nil {
			continue
		}
		plan.Tasks = append(plan.Tasks, PlanTaskState{
			TaskID:      stringValue(tMap["taskId"]),
			Description: stringValue(tMap["description"]),
			Status:      stringValue(tMap["status"]),
		})
	}
	return plan
}

func parseArtifactFromStep(raw map[string]any) *ArtifactState {
	return &ArtifactState{Items: artifactItemsFromValue(raw["items"])}
}
