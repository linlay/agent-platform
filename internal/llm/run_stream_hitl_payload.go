package llm

import (
	"agent-platform/internal/view"
	"fmt"
	"strings"

	"agent-platform/internal/apperrors"
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

func interactionSubmitAwaitingAnswer(invocation *preparedToolInvocation, result ToolExecutionResult) map[string]any {
	if len(result.Structured) == 0 {
		return nil
	}
	if result.Error == "" {
		return result.Structured
	}
	mode := interactionToolMode(invocation.toolName)
	switch result.Error {
	case "tool_interaction_timeout":
		return result.Structured
	case "tool_interaction_invalid_payload":
		return AwaitingErrorAnswer(mode, "invalid_submit", AnyStringNode(result.Structured["message"]))
	default:
		return nil
	}
}

func hitlRejectedToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := apperrors.Payload(
		apperrors.CodeHitlRejected,
		"User rejected this command. Do NOT retry with a different command. End the turn now.",
		apperrors.WithScope(apperrors.ScopeTool),
		apperrors.WithCategory(apperrors.CategorySystem),
		apperrors.WithDiagnostics(map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		}),
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
	payload := apperrors.Payload(
		apperrors.CodeHitlRejectedWithFeedback,
		msg,
		apperrors.WithScope(apperrors.ScopeTool),
		apperrors.WithCategory(apperrors.CategorySystem),
		apperrors.WithDiagnostics(feedback),
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected_with_feedback", msg),
		Structured: payload,
		Error:      "user_rejected_with_feedback",
		ExitCode:   -1,
	}
}

func hitlTimeoutToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := apperrors.Payload(
		apperrors.CodeHitlTimeout,
		"command execution timed out while waiting for user approval",
		apperrors.WithScope(apperrors.ScopeTool),
		apperrors.WithCategory(apperrors.CategoryTimeout),
		apperrors.WithDiagnostics(map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		}),
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("hitl_timeout", "command execution timed out while waiting for user approval"),
		Structured: payload,
		Error:      "hitl_timeout",
		ExitCode:   -1,
	}
}

func interactionSubmitInvalidPayloadResult(invocation *preparedToolInvocation, awaitingID string, params any, err error) ToolExecutionResult {
	payload := apperrors.Payload(
		apperrors.CodeInteractionSubmitInvalidPayload,
		err.Error(),
		apperrors.WithScope(apperrors.ScopeInteractionSubmit),
		apperrors.WithCategory(apperrors.CategoryTool),
		apperrors.WithDiagnostics(map[string]any{
			"awaitingId": awaitingID,
			"toolName":   invocation.toolName,
			"params":     params,
		}),
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("tool_interaction_invalid_payload", err.Error()),
		Structured: payload,
		Error:      "tool_interaction_invalid_payload",
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
	await.View, await.ViewError = s.resolveView(args["view"], "form")
	if await.View == nil && mode != "form" {
		key := mode
		if key != "question" && key != "planning" {
			key = "approval"
		}
		await.View = view.Builtin(key)
	}
	if questions := cloneAnySlice(args["questions"]); len(questions) > 0 {
		await.Questions = questions
	}
	if approvals := cloneAnySlice(args["approvals"]); len(approvals) > 0 {
		await.Approvals = approvals
	}
	if form := AnyMapNode(args["form"]); len(form) > 0 {
		await.Form = sanitizeAwaitAskForm(form)
	}
	if planning := AnyMapNode(args["planning"]); len(planning) > 0 {
		await.Planning = CloneMap(planning)
	}
	return await
}

// The command stays server-side; the client only receives the form title and data.
func sanitizeAwaitAskForm(form map[string]any) map[string]any {
	entry := CloneMap(form)
	delete(entry, "command")
	return entry
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
	summaries, truncated := SummarizeApprovals(awaitAsk.Approvals)
	return AwaitingSubmitContext{
		Summaries:          summaries,
		SummariesTruncated: truncated,
		AwaitingID:         awaitAsk.AwaitingID,
		Mode:               awaitAsk.Mode,
		ItemCount:          awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals),
		Questions:          append([]any(nil), awaitAsk.Questions...),
		View:               awaitAsk.View,
		Form:               awaitAsk.Form,
		Timeout:            awaitAsk.Timeout,
	}
}

func awaitingContextFromDeltaAsk(awaitAsk DeltaAwaitAsk) AwaitingSubmitContext {
	summaries, truncated := SummarizeApprovals(awaitAsk.Approvals)
	return AwaitingSubmitContext{
		Summaries:          summaries,
		SummariesTruncated: truncated,
		AwaitingID:         awaitAsk.AwaitingID,
		Mode:               awaitAsk.Mode,
		ItemCount:          awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals),
		Questions:          append([]any(nil), awaitAsk.Questions...),
		View:               awaitAsk.View,
		Form:               awaitAsk.Form,
		Timeout:            awaitAsk.Timeout,
	}
}

// awaitItemCount is the expected params length; planning and form take one param.
func awaitItemCount(mode string, questions []any, approvals []any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form", "planning":
		return 1
	default:
		return 0
	}
}
