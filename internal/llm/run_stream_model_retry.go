package llm

import (
	"time"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
)

const modelActivityPhase = "model_call"

func (s *llmRunStream) modelMaxAttempts() int {
	if s == nil || s.execCtx == nil {
		return 1
	}
	budget := NormalizeBudget(s.execCtx.Budget)
	maxAttempts := budget.Model.RetryCount + 1
	if maxAttempts < 1 {
		return 1
	}
	return maxAttempts
}

func (s *llmRunStream) buildModelRunActivity(status string, call *pendingModelCall, err error) DeltaRunActivity {
	if call == nil {
		call = s.modelCall
	}
	attempt := 1
	maxAttempts := 1
	var startedAt time.Time
	if call != nil {
		attempt = call.attempt
		maxAttempts = call.maxAttempts
		startedAt = call.attemptStartedAt
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if attempt < 1 {
		attempt = 1
	}
	payload := modelErrorPayload(err)
	reason := modelActivityReason(status, payload)
	activity := DeltaRunActivity{
		TaskID:  s.modelActivityTaskID(),
		ChatID:  s.session.ChatID,
		Phase:   modelActivityPhase,
		Status:  status,
		Message: modelActivityMessage(status, reason),
	}
	if status == "retrying" {
		retry := map[string]any{
			"attempt":     attempt,
			"maxAttempts": maxAttempts,
		}
		if reason != "" {
			retry["reason"] = reason
		}
		if timeoutSeconds := s.modelActivityTimeoutSeconds(payload); timeoutSeconds > 0 {
			retry["timeoutSeconds"] = timeoutSeconds
		}
		if elapsedMs := modelActivityElapsedMs(startedAt); elapsedMs > 0 {
			retry["elapsedMs"] = elapsedMs
		}
		activity.Retry = retry
	}
	return activity
}

func (s *llmRunStream) appendModelRunActivity(status string, err error) {
	s.pending = append(s.pending, s.buildModelRunActivity(status, s.modelCall, err))
}

func (s *llmRunStream) modelActivityTaskID() string {
	if s == nil {
		return ""
	}
	return s.session.SubTaskID
}

func (s *llmRunStream) modelActivityTimeoutSeconds(payload map[string]any) int64 {
	if seconds := modelActivityPayloadTimeoutSeconds(payload); seconds > 0 {
		return seconds
	}
	timeout := s.modelStreamIdleTimeout()
	if timeout <= 0 {
		return 0
	}
	seconds := int64(timeout / time.Second)
	if seconds <= 0 {
		return 1
	}
	return seconds
}

func modelActivityPayloadTimeoutSeconds(payload map[string]any) int64 {
	if len(payload) == 0 {
		return 0
	}
	diagnostics, _ := payload["diagnostics"].(map[string]any)
	if len(diagnostics) == 0 {
		return 0
	}
	seconds := AnyIntNode(diagnostics["timeoutSeconds"])
	if seconds <= 0 {
		return 0
	}
	return int64(seconds)
}

func modelActivityElapsedMs(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	elapsed := time.Since(startedAt).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func modelErrorPayload(err error) map[string]any {
	if err == nil {
		return nil
	}
	return apperrors.FromError(
		err,
		apperrors.CodeProviderRequestFailed,
		apperrors.WithCategory(apperrors.CategoryModel),
		apperrors.WithScope(apperrors.ScopeModel),
	)
}

func modelActivityReason(status string, payload map[string]any) string {
	if len(payload) > 0 {
		if diagnostics, _ := payload["diagnostics"].(map[string]any); len(diagnostics) > 0 {
			if reason, _ := diagnostics["reason"].(string); reason != "" {
				return reason
			}
		}
		if code, _ := payload["code"].(string); code != "" {
			return code
		}
	}
	switch status {
	case "waiting":
		return "model_call_waiting"
	case "retrying":
		return "model_call_retrying"
	case "running":
		return "model_call_running"
	case "completed":
		return "model_call_completed"
	default:
		return "model_call"
	}
}

func modelActivityMessage(status string, reason string) string {
	switch status {
	case "waiting":
		return "正在等待模型响应"
	case "retrying":
		return "模型响应超时，正在重试"
	case "running":
		return "模型正在响应"
	case "completed":
		return "模型调用完成"
	default:
		return reason
	}
}

func modelErrorRetryable(err error) bool {
	payload := modelErrorPayload(err)
	if len(payload) == 0 {
		return false
	}
	retryable, _ := payload["retryable"].(bool)
	return retryable
}

func (s *llmRunStream) currentModelTurnRetrySafe() bool {
	if s == nil {
		return false
	}
	// Provider deltas may already be visible to the client, but they remain
	// uncommitted and tool invocations are not queued until finishCurrentTurn
	// accepts the model turn. Recovery IDs let the client remove those partial
	// blocks. Once any tool batch has started, retry could duplicate effects.
	return s.activeToolCall == nil &&
		s.activeToolBatch == nil &&
		len(s.queuedToolCalls) == 0 &&
		s.hitlPendingBatch == nil &&
		s.hitlPendingCall == nil
}

func (s *llmRunStream) closeCurrentProviderTurn() {
	if s == nil || s.currentTurn == nil {
		return
	}
	if s.currentTurn.body != nil {
		_ = s.currentTurn.body.Close()
	}
	if s.currentTurn.cancel != nil {
		s.currentTurn.cancel()
	}
	s.currentTurn = nil
}

func (s *llmRunStream) canRetryModelAttempt(err error) bool {
	if s == nil || s.modelCall == nil {
		return false
	}
	if s.modelCall.attempt >= s.modelCall.maxAttempts {
		return false
	}
	if !modelErrorRetryable(err) {
		return false
	}
	return s.currentModelTurnRetrySafe()
}

func (s *llmRunStream) handleModelAttemptError(err error) error {
	if err == nil {
		return nil
	}
	if s.canRetryModelAttempt(err) {
		call := s.modelCall
		nextAttempt := call.attempt + 1
		s.pending = append(s.pending, s.modelTurnDiscardDelta(call, err, true, nextAttempt))
		s.closeCurrentProviderTurn()
		s.modelCall.attempt = nextAttempt
		s.modelCall.attemptStartedAt = time.Time{}
		return nil
	}
	if s.modelCall != nil && s.currentModelTurnRetrySafe() {
		s.pending = append(s.pending, s.modelTurnDiscardDelta(s.modelCall, err, false, s.modelCall.attempt))
	}
	s.closeCurrentProviderTurn()
	s.modelCall = nil
	s.modelTerminalError = err
	return nil
}

func (s *llmRunStream) modelTurnDiscardDelta(call *pendingModelCall, err error, retrying bool, attempt int) DeltaModelTurnDiscard {
	if call == nil {
		call = &pendingModelCall{runSeq: s.runLLMChatCompletionCount, maxAttempts: 1}
	}
	if attempt < 1 {
		attempt = 1
	}
	maxAttempts := call.maxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	payload := modelErrorPayload(err)
	discard := DeltaModelTurnDiscard{
		TaskID:      s.modelActivityTaskID(),
		RunSeq:      call.runSeq,
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		Reason:      modelActivityReason("retrying", payload),
		Retrying:    retrying,
	}
	if retrying {
		discard.TimeoutSeconds = s.modelActivityTimeoutSeconds(payload)
		discard.ElapsedMs = modelActivityElapsedMs(call.attemptStartedAt)
	}
	return discard
}
