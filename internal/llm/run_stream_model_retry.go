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

func (s *llmRunStream) buildModelActivitySnapshot(status string, call *pendingModelCall, err error) DeltaActivitySnapshot {
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
	return DeltaActivitySnapshot{
		TaskID:         s.modelActivityTaskID(),
		ChatID:         s.session.ChatID,
		Phase:          modelActivityPhase,
		Status:         status,
		Attempt:        attempt,
		MaxAttempts:    maxAttempts,
		Reason:         reason,
		Message:        modelActivityMessage(status, reason),
		TimeoutSeconds: s.modelActivityTimeoutSeconds(payload),
		ElapsedMs:      modelActivityElapsedMs(startedAt),
		Error:          payload,
	}
}

func (s *llmRunStream) appendModelActivitySnapshot(status string, err error) {
	s.pending = append(s.pending, s.buildModelActivitySnapshot(status, s.modelCall, err))
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
	case "disconnecting":
		return "model_call_disconnecting"
	case "failed":
		return "model_call_failed"
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
	case "disconnecting":
		return "模型流已断开"
	case "failed":
		return "模型请求失败"
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
	if s == nil || s.currentTurn == nil {
		return true
	}
	turn := s.currentTurn
	if turn.hasMeaningful || !turn.firstVisibleAt.IsZero() {
		return false
	}
	if turn.content.Len() > 0 || turn.reasoning.Len() > 0 || len(turn.toolCalls) > 0 {
		return false
	}
	return len(s.pending) == 0
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
		s.closeCurrentProviderTurn()
		s.modelCall.attempt++
		s.modelCall.attemptStartedAt = time.Time{}
		s.appendModelActivitySnapshot("retrying", err)
		return nil
	}
	status := "failed"
	if !s.currentModelTurnRetrySafe() {
		status = "disconnecting"
	}
	s.appendModelActivitySnapshot(status, err)
	s.closeCurrentProviderTurn()
	s.modelCall = nil
	s.modelTerminalError = err
	return nil
}
