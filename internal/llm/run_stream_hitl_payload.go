package llm

import (
	"fmt"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func buildHITLApprovalPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	return payload
}

func buildHITLFormPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"mode":     "form",
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	if decision.FormPayload != nil {
		payload["submittedPayload"] = decision.FormPayload
	}
	return payload
}

func buildHITLAwaitingID(toolID string) string {
	return "await_" + strings.TrimSpace(toolID)
}

func buildHITLBatchAwaitingID(runID string, turnStep int) string {
	return fmt.Sprintf("await_batch_%s_%d", strings.TrimSpace(runID), turnStep)
}

func hitlTimeoutAnswer(mode string, timeoutSeconds int64) map[string]any {
	return AwaitingTimeoutAnswer(mode, timeoutSeconds, timeoutSeconds)
}

func frontendSubmitAwaitingAnswer(invocation *preparedToolInvocation, result ToolExecutionResult) map[string]any {
	if len(result.Structured) == 0 {
		return nil
	}
	if result.Error == "" {
		return result.Structured
	}
	mode := strings.TrimSpace(AnyStringNode(invocation.args["mode"]))
	switch result.Error {
	case "frontend_submit_timeout":
		return result.Structured
	case "frontend_submit_invalid_payload":
		return AwaitingErrorAnswer(mode, "invalid_submit", AnyStringNode(result.Structured["message"]))
	default:
		return nil
	}
}

func hitlRejectedToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_rejected",
		"User rejected this command. Do NOT retry with a different command. End the turn now.",
		ErrorScopeTool,
		ErrorCategorySystem,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	payload["final"] = true
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected", "User rejected this command. Do NOT retry with a different command. End the turn now."),
		Structured: payload,
		Error:      "user_rejected",
		ExitCode:   -1,
	}
}

func hitlRejectedFormToolResult(invocation *preparedToolInvocation, reason string, form map[string]any) ToolExecutionResult {
	reason = strings.TrimSpace(reason)
	if len(form) == 0 && reason == "" {
		return hitlRejectedToolResult(invocation)
	}
	feedback := map[string]any{
		"status": "rejected_with_feedback",
		"toolId": invocation.toolID,
	}
	if reason != "" {
		feedback["reason"] = reason
	}
	if len(form) > 0 {
		feedback["revisedForm"] = form
	}
	msg := "User rejected this command with feedback. Review the reason and revised form, then try again with corrections."
	payload := NewErrorPayload(
		"hitl_rejected_with_feedback",
		msg,
		ErrorScopeTool,
		ErrorCategorySystem,
		feedback,
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected_with_feedback", msg),
		Structured: payload,
		Error:      "user_rejected_with_feedback",
		ExitCode:   -1,
	}
}

func hitlTimeoutToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_timeout",
		"command execution timed out while waiting for user approval",
		ErrorScopeTool,
		ErrorCategoryTimeout,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("hitl_timeout", "command execution timed out while waiting for user approval"),
		Structured: payload,
		Error:      "hitl_timeout",
		ExitCode:   -1,
	}
}

func frontendSubmitInvalidPayloadResult(invocation *preparedToolInvocation, awaitingID string, params any, err error) ToolExecutionResult {
	payload := NewErrorPayload(
		"frontend_submit_invalid_payload",
		err.Error(),
		ErrorScopeFrontendSubmit,
		ErrorCategoryTool,
		map[string]any{
			"awaitingId": awaitingID,
			"toolName":   invocation.toolName,
			"params":     params,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("frontend_submit_invalid_payload", err.Error()),
		Structured: payload,
		Error:      "frontend_submit_invalid_payload",
		ExitCode:   -1,
	}
}

func (s *llmRunStream) buildHITLAwaitDelta(awaitingID string, args map[string]any, ruleTimeout int) DeltaAwaitAsk {
	mode := strings.ToLower(strings.TrimSpace(AnyStringNode(args["mode"])))
	timeout := s.resolveHITLTimeoutWithItem(mode, int64(ruleTimeout))
	await := DeltaAwaitAsk{
		AwaitingID: awaitingID,
		Mode:       mode,
		Timeout:    timeout,
		RunID:      s.session.RunID,
	}
	await.ViewportType = strings.TrimSpace(AnyStringNode(args["viewportType"]))
	await.ViewportKey = strings.TrimSpace(AnyStringNode(args["viewportKey"]))
	switch await.Mode {
	case "question":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "question"
		}
	case "approval":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "approval"
		}
	case "form":
		if await.ViewportType == "" {
			await.ViewportType = "html"
		}
	case "plan":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "plan"
		}
	}
	if questions := cloneAnySlice(args["questions"]); len(questions) > 0 {
		await.Questions = questions
	}
	if approvals := cloneAnySlice(args["approvals"]); len(approvals) > 0 {
		await.Approvals = approvals
	}
	if forms := cloneAnySlice(args["forms"]); len(forms) > 0 {
		await.Forms = sanitizeAwaitAskForms(forms)
	}
	if plan := AnyMapNode(args["plan"]); len(plan) > 0 {
		await.Plan = CloneMap(plan)
	}
	return await
}

func sanitizeAwaitAskForms(forms []any) []any {
	cloned := make([]any, 0, len(forms))
	for _, item := range forms {
		form := AnyMapNode(item)
		if len(form) == 0 {
			continue
		}
		entry := CloneMap(form)
		delete(entry, "command")
		cloned = append(cloned, entry)
	}
	return cloned
}

func cloneAnySlice(raw any) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case map[string]any:
			cloned = append(cloned, CloneMap(value))
		default:
			cloned = append(cloned, value)
		}
	}
	return cloned
}

func firstAwaitItem(raw any) map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		for _, item := range typed {
			if len(item) > 0 {
				return item
			}
		}
	case []any:
		for _, item := range typed {
			entry := AnyMapNode(item)
			if len(entry) > 0 {
				return entry
			}
		}
	}
	return nil
}

func (s *llmRunStream) normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return normalizeHITLSubmit(args, params)
}

func awaitingContextFromStreamAsk(awaitAsk *stream.AwaitAsk) AwaitingSubmitContext {
	if awaitAsk == nil {
		return AwaitingSubmitContext{}
	}
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Plan),
		Questions:  append([]any(nil), awaitAsk.Questions...),
		Timeout:    awaitAsk.Timeout,
	}
}

func awaitingContextFromDeltaAsk(awaitAsk DeltaAwaitAsk) AwaitingSubmitContext {
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Plan),
		Questions:  append([]any(nil), awaitAsk.Questions...),
		Timeout:    awaitAsk.Timeout,
	}
}

func awaitItemCount(mode string, questions []any, approvals []any, forms []any, plan map[string]any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form":
		return len(forms)
	case "plan":
		if len(plan) > 0 {
			return 1
		}
		return 0
	default:
		return 0
	}
}
