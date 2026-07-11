package stream

func (d *StreamEventDispatcher) handleContentDelta(input ContentDelta) []StreamEvent {
	taskID := d.resolveTaskID(input.TaskID)
	scope := taskScope(taskID)
	events := d.closeForSwitch("content", taskID)
	active, ok := d.state.activeContents[scope]
	if !ok || active.ID != input.ContentID {
		d.state.activeContents[scope] = activeContentState{
			ID: input.ContentID,
			Block: contentBlockState{
				TaskID:       taskID,
				ActorType:    input.ActorType,
				TeamID:       input.TeamID,
				AgentKey:     input.AgentKey,
				Presentation: input.Presentation,
			},
		}
		d.state.lastContentID = input.ContentID
		payload := map[string]any{
			"contentId": input.ContentID,
			"runId":     d.request.RunID,
			"taskId":    taskID,
		}
		appendContentActorPayload(payload, input.ActorType, input.TeamID, input.AgentKey, input.Presentation)
		events = append(events, NewEvent("content.start", payload))
	}
	d.state.contentSeen = true
	d.state.lastContentID = input.ContentID
	d.state.contentBuffer[input.ContentID] += input.Delta
	d.state.fullContent += input.Delta
	payload := map[string]any{
		"contentId": input.ContentID,
		"delta":     input.Delta,
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	appendContentActorPayload(payload, input.ActorType, input.TeamID, input.AgentKey, input.Presentation)
	events = append(events, NewEvent("content.delta", payload))
	return events
}

func (d *StreamEventDispatcher) closeContent() []StreamEvent {
	if len(d.state.activeContents) == 0 {
		return nil
	}
	var events []StreamEvent
	for scope := range d.state.activeContents {
		events = append(events, d.closeContentScope(scope)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeContentScope(scope string) []StreamEvent {
	active, ok := d.state.activeContents[scope]
	if !ok {
		return nil
	}
	delete(d.state.activeContents, scope)
	contentID := active.ID
	block := active.Block
	endPayload := map[string]any{
		"contentId": contentID,
	}
	appendContentActorPayload(endPayload, block.ActorType, block.TeamID, block.AgentKey, block.Presentation)
	events := []StreamEvent{NewEvent("content.end", endPayload)}
	if d.state.contentSeen {
		payload := map[string]any{
			"contentId": contentID,
			"runId":     d.request.RunID,
			"text":      d.state.contentBuffer[contentID],
			"taskId":    block.TaskID,
		}
		appendContentActorPayload(payload, block.ActorType, block.TeamID, block.AgentKey, block.Presentation)
		events = append(events, NewEvent("content.snapshot", payload))
	}
	return events
}

func appendContentActorPayload(payload map[string]any, actorType string, teamID string, agentKey string, presentation string) {
	if payload == nil {
		return
	}
	actor := map[string]any{}
	if actorType != "" {
		actor["type"] = actorType
	}
	if teamID != "" {
		payload["teamId"] = teamID
		actor["teamId"] = teamID
	}
	if agentKey != "" {
		payload["agentKey"] = agentKey
		actor["agentKey"] = agentKey
	}
	if len(actor) > 0 {
		payload["actor"] = actor
	}
	if presentation != "" {
		payload["presentation"] = presentation
	}
}
