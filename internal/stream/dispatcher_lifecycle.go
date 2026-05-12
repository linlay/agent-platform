package stream

func (d *StreamEventDispatcher) Complete() []StreamEvent {
	if d.state.terminated {
		return nil
	}
	events := d.closeOpenBlocks()
	if d.state.runError != nil {
		payload := map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
		}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.error", payload))
		d.state.terminated = true
		return events
	}
	completePayload := map[string]any{
		"runId":        d.request.RunID,
		"finishReason": d.state.runFinishReason,
	}
	if usage := d.usagePayload(); usage != nil {
		completePayload["usage"] = usage
	}
	events = append(events, NewEvent("run.complete", completePayload))
	d.state.terminated = true
	return events
}

func (d *StreamEventDispatcher) Fail(err error) []StreamEvent {
	if d.state.terminated {
		return nil
	}
	d.state.runError = map[string]any{
		"code":     "stream_failed",
		"message":  err.Error(),
		"scope":    "run",
		"category": "runtime",
	}
	events := d.closeOpenBlocks()
	payload := map[string]any{
		"runId": d.request.RunID,
		"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
	}
	if usage := d.usagePayload(); usage != nil {
		payload["usage"] = usage
	}
	events = append(events, NewEvent("run.error", payload))
	d.state.terminated = true
	return events
}

func (d *StreamEventDispatcher) closeForSwitch(next string) []StreamEvent {
	switch next {
	case "reasoning":
		return append(d.closeContent(), append(d.closeAllTools(), d.closeAllActions()...)...)
	case "content":
		return append(d.closeReasoning(), append(d.closeAllTools(), d.closeAllActions()...)...)
	case "tool":
		return append(d.closeReasoning(), append(d.closeContent(), d.closeAllActions()...)...)
	case "action":
		return append(d.closeReasoning(), append(d.closeContent(), d.closeAllTools()...)...)
	default:
		return d.closeOpenBlocks()
	}
}

func (d *StreamEventDispatcher) usagePayload() map[string]any {
	if d.state.runUsage == nil || d.state.runUsage.TotalTokens == 0 {
		return nil
	}
	return map[string]any{
		"promptTokens":     d.state.runUsage.PromptTokens,
		"completionTokens": d.state.runUsage.CompletionTokens,
		"totalTokens":      d.state.runUsage.TotalTokens,
	}
}

func (d *StreamEventDispatcher) closeOpenBlocks() []StreamEvent {
	events := d.closeReasoning()
	events = append(events, d.closeContent()...)
	events = append(events, d.closeAllTools()...)
	events = append(events, d.closeAllActions()...)
	return events
}

func normalizeErrorMap(input map[string]any, defaultCode string, defaultScope string, defaultCategory string) map[string]any {
	output := clonePayload(input)
	if output == nil {
		output = map[string]any{}
	}
	if _, ok := output["code"]; !ok {
		output["code"] = defaultCode
	}
	if _, ok := output["message"]; !ok {
		output["message"] = ""
	}
	if _, ok := output["scope"]; !ok {
		output["scope"] = defaultScope
	}
	if _, ok := output["category"]; !ok {
		output["category"] = defaultCategory
	}
	return output
}
