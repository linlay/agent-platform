package stream

import "agent-platform/internal/apperrors"

func (d *StreamEventDispatcher) Complete() []StreamEvent {
	if d.state.terminated {
		return nil
	}
	events := d.closeOpenBlocks()
	if d.state.runError != nil {
		payload := map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(d.state.runError, string(apperrors.CodeStreamFailed), string(apperrors.ScopeRun), string(apperrors.CategoryChatRun)),
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
	d.state.runError = apperrors.FromError(
		err,
		apperrors.CodeStreamFailed,
		apperrors.WithScope(apperrors.ScopeRun),
		apperrors.WithCategory(apperrors.CategoryChatRun),
	)
	events := d.closeOpenBlocks()
	payload := map[string]any{
		"runId": d.request.RunID,
		"error": normalizeErrorMap(d.state.runError, string(apperrors.CodeStreamFailed), string(apperrors.ScopeRun), string(apperrors.CategoryChatRun)),
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
	if d.state.runUsage == nil || (d.state.runUsage.TotalTokens == 0 && d.state.runUsage.LLMChatCompletionCount == 0 && d.state.runUsage.ToolCallCount == 0) {
		return nil
	}
	return usageMap(d.state.runUsage)
}

func usageMap(usage *runUsageState) map[string]any {
	if usage == nil {
		return nil
	}
	out := map[string]any{
		"promptTokens":     usage.PromptTokens,
		"completionTokens": usage.CompletionTokens,
		"totalTokens":      usage.TotalTokens,
	}
	addDetailedUsage(out, usage.ReasoningTokens, usage.PromptCacheHitTokens, usage.PromptCacheMissTokens)
	if usage.LLMChatCompletionCount > 0 {
		out["llmChatCompletionCount"] = usage.LLMChatCompletionCount
	}
	if usage.ToolCallCount > 0 {
		out["toolCallCount"] = usage.ToolCallCount
	}
	return out
}

func addDetailedUsage(out map[string]any, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int) {
	if out == nil {
		return
	}
	promptDetails := map[string]any{}
	if promptCacheHitTokens > 0 {
		promptDetails["cacheHitTokens"] = promptCacheHitTokens
	}
	if promptCacheMissTokens > 0 {
		promptDetails["cacheMissTokens"] = promptCacheMissTokens
	} else if promptTokens := intValue(out["promptTokens"]); promptCacheHitTokens > 0 && promptTokens > promptCacheHitTokens {
		promptDetails["cacheMissTokens"] = promptTokens - promptCacheHitTokens
	}
	if len(promptDetails) > 0 {
		out["promptTokensDetails"] = promptDetails
	}
	if reasoningTokens > 0 {
		out["completionTokensDetails"] = map[string]any{"reasoningTokens": reasoningTokens}
	}
}

func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
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
	code, _ := output["code"].(string)
	if code == "" {
		code = defaultCode
		output["code"] = defaultCode
	}
	if _, ok := output["message"]; !ok {
		output["message"] = ""
	}
	definition, known := apperrors.Lookup(apperrors.Code(code))
	if _, ok := output["scope"]; !ok {
		if known {
			output["scope"] = string(definition.Scope)
		} else {
			output["scope"] = defaultScope
		}
	}
	if _, ok := output["category"]; !ok {
		if known {
			output["category"] = string(definition.Category)
		} else {
			output["category"] = defaultCategory
		}
	}
	if _, ok := output["status"]; !ok && known {
		output["status"] = definition.HTTPStatus
	}
	if _, ok := output["retryable"]; !ok && known {
		output["retryable"] = definition.Retryable
	}
	if _, ok := output["userSafeMessageKey"]; !ok && known {
		output["userSafeMessageKey"] = definition.UserSafeMessageKey
	}
	return output
}
