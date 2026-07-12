package chat

import (
	"strings"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/plantasks"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

type replayMessageOptions struct {
	HideTeamCoordinatorInternals bool
	ActorType                    string
	TeamID                       string
	AgentKey                     string
	Presentation                 string
}

func storedMessageToEvents(msg map[string]any, runID, taskID, stage string, liveSeq int64, nextSeq func() int64) ([]stream.EventData, error) {
	return storedMessageToEventsWithOptions(msg, runID, taskID, stage, liveSeq, nextSeq, replayMessageOptions{})
}

func storedMessageToEventsWithOptions(msg map[string]any, runID, taskID, stage string, liveSeq int64, nextSeq func() int64, options replayMessageOptions) ([]stream.EventData, error) {
	role, _ := msg["role"].(string)
	ts, err := timecontract.ParseEpochMillis(msg["ts"], "ts", "chat.replay.message.ts")
	if err != nil {
		return nil, err
	}
	var events []stream.EventData
	options = replayMessageActorOptions(msg, options)

	switch role {
	case "assistant":
		if rc, ok := msg["reasoning_content"]; ok && !options.HideTeamCoordinatorInternals {
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
				decorateReplayActor(payload, options)
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
				decorateReplayActor(payload, options)
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
				if options.HideTeamCoordinatorInternals || agentteam.IsHiddenTool(fnName) {
					continue
				}
				fnArgs, _ := fn["arguments"].(string)
				actionID, _ := tcMap["_actionId"].(string)
				toolID, _ := tcMap["_toolId"].(string)

				if actionID != "" {
					payload := map[string]any{
						"actionId":   actionID,
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
		if options.HideTeamCoordinatorInternals || agentteam.IsHiddenTool(stringFromAny(msg["name"])) {
			return nil, nil
		}
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

	return events, nil
}

func replayMessageActorOptions(msg map[string]any, options replayMessageOptions) replayMessageOptions {
	if value := strings.TrimSpace(stringFromAny(msg["actorType"])); value != "" {
		options.ActorType = value
	}
	if value := strings.TrimSpace(stringFromAny(msg["teamId"])); value != "" {
		options.TeamID = value
	}
	if value := strings.TrimSpace(stringFromAny(msg["agentKey"])); value != "" {
		options.AgentKey = value
	}
	if value := strings.TrimSpace(stringFromAny(msg["presentation"])); value != "" {
		options.Presentation = value
	}
	return options
}

func decorateReplayActor(payload map[string]any, options replayMessageOptions) {
	if payload == nil || strings.TrimSpace(options.ActorType) == "" {
		return
	}
	actor := map[string]any{"type": strings.TrimSpace(options.ActorType)}
	if strings.TrimSpace(options.TeamID) != "" {
		actor["teamId"] = strings.TrimSpace(options.TeamID)
		payload["teamId"] = strings.TrimSpace(options.TeamID)
	}
	if strings.TrimSpace(options.AgentKey) != "" {
		actor["agentKey"] = strings.TrimSpace(options.AgentKey)
		payload["agentKey"] = strings.TrimSpace(options.AgentKey)
	}
	payload["actor"] = actor
	if strings.TrimSpace(options.Presentation) != "" {
		payload["presentation"] = strings.TrimSpace(options.Presentation)
	}
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

func planStateFromTaskSnapshot(snapshot *plantasks.Snapshot) *PlanState {
	if snapshot == nil {
		return nil
	}
	plan := &PlanState{PlanID: snapshot.PlanID, Tasks: []PlanTaskState{}}
	for _, task := range snapshot.Tasks {
		plan.Tasks = append(plan.Tasks, PlanTaskState{
			TaskID:      task.TaskID,
			Description: task.Description,
			Status:      task.Status,
		})
	}
	return plan
}

func parseArtifactFromStep(raw map[string]any) *ArtifactState {
	return &ArtifactState{Items: artifactItemsFromValue(raw["items"])}
}
