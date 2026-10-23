package llm

import (
	"fmt"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/resultsubmit"
)

// resultSubmitReminder is the one reminder a Run started through /api/execute
// gets when the model answers without delivering its result.
const resultSubmitReminder = "This task must end with a result_submit call: status success with output, or status failure with reason. Call result_submit now."

// observeResultSubmit ends the Run after an accepted result_submit. A Run
// started through /api/execute also counts rejected submissions and fails
// once they are used up.
func (s *llmRunStream) observeResultSubmit(invocation *preparedToolInvocation, result ToolExecutionResult) {
	if invocation == nil || !IsResultSubmitToolName(invocation.toolName) {
		return
	}
	if result.ExitCode == 0 && result.Error == "" {
		s.stopAfterToolBatch = true
		return
	}
	if s.session.ResultContract == nil {
		return
	}
	s.resultSubmitRejected++
	if s.resultSubmitRejected >= resultsubmit.MaxInvalidSubmissions {
		s.stopAfterToolBatch = true
	}
}

// resultSubmitExhaustedError is the terminal error of a Run whose rejected
// submissions are used up, or nil.
func (s *llmRunStream) resultSubmitExhaustedError() map[string]any {
	if s.session.ResultContract == nil || s.session.ResultContract.Submission() != nil || s.resultSubmitRejected < resultsubmit.MaxInvalidSubmissions {
		return nil
	}
	return apperrors.Payload(
		apperrors.CodeOutputInvalid,
		fmt.Sprintf("result_submit was rejected %d times", s.resultSubmitRejected),
		apperrors.WithScope(apperrors.ScopeRun),
	)
}

// resultSubmitMissing decides a model answer that ends a Run started through
// /api/execute without a result: the first time the model is reminded once,
// after that the Run fails with output_missing.
func (s *llmRunStream) resultSubmitMissing() (remind bool, failure map[string]any) {
	contract := s.session.ResultContract
	if contract == nil || contract.Submission() != nil {
		return false, nil
	}
	if !s.resultSubmitReminded && s.allowToolUse && !s.finalTurnAttempted {
		s.resultSubmitReminded = true
		return true, nil
	}
	return false, apperrors.Payload(
		apperrors.CodeOutputMissing,
		"the Run ended without calling result_submit",
		apperrors.WithScope(apperrors.ScopeRun),
	)
}

// failUnattendedApproval replaces an approval request in a Run started through
// /api/execute: nobody can answer it, so the Run fails with approval_required
// instead of waiting. Questions and forms are not approvals and still wait.
// It covers one reviewed invocation and a batch of queued ones alike.
func (s *llmRunStream) failUnattendedApproval(invocations ...*preparedToolInvocation) bool {
	if s.session.ResultContract == nil || len(invocations) == 0 {
		return false
	}
	var payload map[string]any
	for _, invocation := range invocations {
		if invocation == nil {
			continue
		}
		message := fmt.Sprintf("%s needs approval, which a Run started through /api/execute cannot wait for", invocation.toolName)
		itemPayload := apperrors.Payload(
			apperrors.CodeApprovalRequired,
			message,
			apperrors.WithScope(apperrors.ScopeTool),
			apperrors.WithDiagnostics(map[string]any{
				"toolId":   invocation.toolID,
				"toolName": invocation.toolName,
			}),
		)
		if payload == nil {
			payload = itemPayload
		}
		s.appendOriginalToolResult(invocation, ToolExecutionResult{
			Output:     formatToolErrorOutput(string(apperrors.CodeApprovalRequired), message),
			Structured: itemPayload,
			Error:      string(apperrors.CodeApprovalRequired),
			ExitCode:   -1,
		})
		s.finishToolInvocation(invocation)
	}
	if payload == nil {
		return false
	}
	s.queuedToolCalls = nil
	s.enqueueTerminalRunError(payload)
	return true
}
