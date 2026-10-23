package stream

import (
	"strings"

	"agent-platform/internal/view"
)

func (d *StreamEventDispatcher) handleContentDelta(input ContentDelta) []StreamEvent {
	taskID := d.resolveTaskID(input.TaskID)
	scope := taskScope(taskID)
	events := d.closeForSwitch("content", taskID)
	active, ok := d.state.activeContents[scope]
	if ok && active.ID != input.ContentID {
		// A replaced block must still end, or its snapshot is never persisted.
		events = append(events, d.closeContentScope(scope)...)
		ok = false
	}
	if !ok {
		d.state.activeContents[scope] = activeContentState{
			ID: input.ContentID,
			Block: contentBlockState{
				TaskID:    taskID,
				ActorType: input.ActorType,

				AgentKey:     input.AgentKey,
				Presentation: input.Presentation,
			},
		}
		d.state.lastContentID = input.ContentID
		d.state.contentGuards[input.ContentID] = newMarkdownDestinationGuard(d.request.ChatID)
		payload := map[string]any{
			"contentId": input.ContentID,
			"runId":     d.request.RunID,
			"taskId":    taskID,
		}
		appendContentActorPayload(payload, input.ActorType, input.AgentKey, input.Presentation)
		events = append(events, NewEvent("content.start", payload))
	}
	for _, event := range events {
		if event.Type == "content.start" {
			d.state.blockStarts["content:"+input.ContentID] = event
		}
	}
	guard := d.state.contentGuards[input.ContentID]
	if guard == nil {
		guard = newMarkdownDestinationGuard(d.request.ChatID)
		d.state.contentGuards[input.ContentID] = guard
	}
	if input.BindView != nil && d.state.contentBinders[input.ContentID] == nil {
		d.state.contentBinders[input.ContentID] = input.BindView
	}
	safeDelta := guard.Write(input.Delta)
	if safeDelta == "" {
		return events
	}
	d.state.contentSeen = true
	d.state.lastContentID = input.ContentID
	d.state.contentBuffer[input.ContentID] += safeDelta
	d.state.fullContent += safeDelta
	payload := map[string]any{
		"contentId": input.ContentID,
		"delta":     safeDelta,
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	appendContentActorPayload(payload, input.ActorType, input.AgentKey, input.Presentation)
	events = append(events, NewEvent("content.delta", payload))
	if strings.Contains(safeDelta, "\n") {
		events = append(events, d.bindContentViews(input.ContentID, d.state.activeContents[scope].Block, false)...)
	}
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
	var events []StreamEvent
	if guard := d.state.contentGuards[contentID]; guard != nil {
		if tail := guard.Flush(); tail != "" {
			d.state.contentSeen = true
			d.state.contentBuffer[contentID] += tail
			d.state.fullContent += tail
			payload := map[string]any{"contentId": contentID, "delta": tail}
			if block.TaskID != "" {
				payload["taskId"] = block.TaskID
			}
			appendContentActorPayload(payload, block.ActorType, block.AgentKey, block.Presentation)
			events = append(events, NewEvent("content.delta", payload))
		}
	}
	delete(d.state.contentGuards, contentID)
	events = append(events, d.bindContentViews(contentID, block, true)...)
	delete(d.state.contentBinders, contentID)
	endPayload := map[string]any{
		"contentId": contentID,
	}
	appendContentActorPayload(endPayload, block.ActorType, block.AgentKey, block.Presentation)
	events = append(events, NewEvent("content.end", endPayload))
	if d.state.contentSeen {
		payload := map[string]any{
			"contentId": contentID,
			"runId":     d.request.RunID,
			"text":      d.state.contentBuffer[contentID],
			"taskId":    block.TaskID,
		}
		if views := d.state.contentViews[contentID]; len(views) > 0 {
			payload["views"] = views
		}
		appendContentActorPayload(payload, block.ActorType, block.AgentKey, block.Presentation)
		events = append(events, NewEvent("content.snapshot", payload))
	}
	return events
}

func appendContentActorPayload(payload map[string]any, actorType string, agentKey string, presentation string) {
	if payload == nil {
		return
	}
	actor := map[string]any{}
	if actorType != "" {
		actor["type"] = actorType
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

// bindContentViews freezes newly closed Markdown view blocks of one content
// block. The text is never rewritten: bindings are published beside it as
// content.view events and persisted on content.snapshot.
func (d *StreamEventDispatcher) bindContentViews(contentID string, block contentBlockState, final bool) []StreamEvent {
	binder := d.state.contentBinders[contentID]
	text := d.state.contentBuffer[contentID]
	if binder == nil || !strings.Contains(strings.ToLower(text), "```view") {
		return nil
	}
	blocks := view.ParseMarkdownBlocks(text, final)
	bound := d.state.contentViews[contentID]
	var events []StreamEvent
	for _, markdown := range blocks[min(len(bound), len(blocks)):] {
		binding := map[string]any{"index": markdown.Index}
		err := markdown.Err
		if markdown.Reference != nil {
			binding["view"] = markdown.Reference.Map()
		}
		if err == nil {
			resolved, bindErr := binder(*markdown.Reference, markdown.Query, markdown.Data)
			if resolved.Key != "" {
				binding["view"] = resolved.Map()
			}
			err = bindErr
		}
		if code := view.ErrorCode(err); code != "" {
			binding["viewError"] = code
		}
		d.state.contentViews[contentID] = append(d.state.contentViews[contentID], binding)
		payload := map[string]any{"contentId": contentID, "runId": d.request.RunID}
		for key, value := range binding {
			payload[key] = value
		}
		if block.TaskID != "" {
			payload["taskId"] = block.TaskID
		}
		appendContentActorPayload(payload, block.ActorType, block.AgentKey, block.Presentation)
		events = append(events, NewEvent("content.view", payload))
	}
	return events
}
