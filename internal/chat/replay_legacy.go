package chat

import "agent-platform/internal/stream"

// ---------------------------------------------------------------------------
// Legacy format: raw SSE events with "type" field (old Go format)
// ---------------------------------------------------------------------------

func parseChatLegacyFormat(summary Summary, events []map[string]any, rawMessages []map[string]any) (Detail, error) {
	events = rebuildSnapshotEvents(events)

	plan, artifact := deriveRunState(events)
	orderedEvents := make([]stream.EventData, 0, len(events))
	for _, event := range events {
		eventType, _ := event["type"].(string)
		if eventType == "plan.create" || eventType == "plan.update" || eventType == "artifact.publish" {
			continue
		}
		orderedEvents = append(orderedEvents, stream.EventDataFromMap(event))
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		RawMessages: rawMessages,
		Events:      orderedEvents,
		Plan:        plan,
		Artifact:    artifact,
	}, nil
}

// ---------------------------------------------------------------------------
// Legacy format helpers
// ---------------------------------------------------------------------------

func deriveRunState(events []map[string]any) (*PlanState, *ArtifactState) {
	var plan *PlanState
	var artifact *ArtifactState
	for _, event := range events {
		eventType, _ := event["type"].(string)
		switch eventType {
		case "plan.create", "plan.update":
			planID, _ := event["planId"].(string)
			next := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}
			rawPlan := event["plan"]
			if items, ok := rawPlan.([]any); ok {
				for _, item := range items {
					mapped, _ := item.(map[string]any)
					if mapped == nil {
						continue
					}
					next.Tasks = append(next.Tasks, PlanTaskState{
						TaskID:      stringValue(mapped["taskId"]),
						Description: stringValue(mapped["description"]),
						Status:      stringValue(mapped["status"]),
					})
				}
				plan = next
				continue
			}
			if rawMap, ok := rawPlan.(map[string]any); ok {
				var rawTasks any
				rawTasks = rawMap["tasks"]
				if rawTasks == nil {
					rawTasks = rawMap["plan"]
				}
				if items, ok := rawTasks.([]any); ok {
					for _, item := range items {
						mapped, _ := item.(map[string]any)
						if mapped == nil {
							continue
						}
						next.Tasks = append(next.Tasks, PlanTaskState{
							TaskID:      stringValue(mapped["taskId"]),
							Description: stringValue(mapped["description"]),
							Status:      stringValue(mapped["status"]),
						})
					}
				}
			}
			plan = next
		case "artifact.publish":
			if artifact == nil {
				artifact = &ArtifactState{}
			}
			artifact.Items = append(artifact.Items, artifactItemsFromEventPayload(event)...)
		}
	}
	return plan, artifact
}
