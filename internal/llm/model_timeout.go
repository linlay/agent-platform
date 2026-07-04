package llm

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
)

type sseFrameReadResult struct {
	eventName string
	rawChunk  string
	err       error
}

func (s *llmRunStream) modelStreamIdleTimeout() time.Duration {
	if s == nil {
		return 0
	}
	seconds := 0
	if s.execCtx != nil {
		budget := NormalizeBudget(s.execCtx.Budget)
		seconds = budget.Model.Timeout
	}
	if s.model.Timeout > 0 {
		seconds = s.model.Timeout
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *llmRunStream) readCurrentSSEFrame() (string, string, error) {
	if s == nil || s.currentTurn == nil || s.currentTurn.reader == nil {
		return "", "", io.EOF
	}
	return readSSEFrameWithIdleTimeout(s.currentTurn.reader, s.currentTurn.body, s.modelStreamIdleTimeout())
}

func readSSEFrameWithIdleTimeout(reader *bufio.Reader, closer io.Closer, timeout time.Duration) (string, string, error) {
	if timeout <= 0 {
		return readSSEFrame(reader)
	}
	resultCh := make(chan sseFrameReadResult, 1)
	go func() {
		eventName, rawChunk, err := readSSEFrame(reader)
		resultCh <- sseFrameReadResult{eventName: eventName, rawChunk: rawChunk, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result.eventName, result.rawChunk, result.err
	case <-timer.C:
		if closer != nil {
			_ = closer.Close()
		}
		return "", "", modelStreamIdleTimeoutError(timeout)
	}
}

func modelStreamIdleTimeoutError(timeout time.Duration) error {
	seconds := int64(timeout / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return apperrors.New(
		apperrors.CodeProviderTimeout,
		fmt.Sprintf("model stream idle timeout after %d seconds", seconds),
		apperrors.WithDiagnostic("timeoutSeconds", seconds),
		apperrors.WithDiagnostic("reason", "model_stream_idle_timeout"),
	)
}
