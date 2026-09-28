package llm

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
)

func TestModelRetryBackoffSchedule(t *testing.T) {
	for i, want := range []time.Duration{0, 500 * time.Millisecond, 2 * time.Second, 8 * time.Second, 32 * time.Second, 128 * time.Second} {
		if got := modelRetryDelay(i + 1); got != want {
			t.Fatalf("attempt %d delay=%s want=%s", i+1, got, want)
		}
	}
	s := newRetryTestStream(&retryProtocolStub{}, 20)
	if s.modelMaxAttempts() != 6 {
		t.Fatal("ordinary retries must stop after five")
	}
}

func TestRetryActivityPrecedesCancelableBackoff(t *testing.T) {
	protocol := &retryProtocolStub{}
	s := newRetryTestStream(protocol, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.ctx = ctx
	s.modelCall = &pendingModelCall{attempt: 1, maxAttempts: 6}
	failure := apperrors.New(apperrors.CodeProviderRateLimited, "rate limited")
	if err := s.handleModelAttemptError(failure); err != nil {
		t.Fatal(err)
	}
	delta, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	discard := delta.(contracts.DeltaModelTurnDiscard)
	if discard.RetryDelayMs != 500 || discard.RetryAt <= time.Now().UnixMilli() || discard.Attempt != 2 {
		t.Fatalf("%#v", discard)
	}
	// Exercise a long wait without actually sleeping for 128 seconds.
	s.modelCall.retryNotBefore = time.Now().Add(128 * time.Second)
	done := make(chan error, 1)
	go func() { done <- s.openPendingModelCall() }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt backoff")
	}
	if protocol.openCount != 0 {
		t.Fatal("request started after cancellation")
	}
}

func TestBackoffWaitsUntilDeadline(t *testing.T) {
	s := newRetryTestStream(&retryProtocolStub{}, 5)
	deadline := time.Now().Add(40 * time.Millisecond)
	s.modelCall = &pendingModelCall{retryNotBefore: deadline}
	if err := s.waitModelRetry(); err != nil {
		t.Fatal(err)
	}
	if time.Now().Before(deadline) {
		t.Fatal("retry woke early")
	}
	if !s.modelCall.retryNotBefore.IsZero() {
		t.Fatal("deadline not cleared")
	}
}

func TestRunInterruptCancelsModelBackoff(t *testing.T) {
	protocol := &retryProtocolStub{}
	s := newRetryTestStream(protocol, 5)
	s.runControl = contracts.NewRunControl(context.Background(), "retry-test")
	s.modelCall = &pendingModelCall{attempt: 6, maxAttempts: 6, retryNotBefore: time.Now().Add(128 * time.Second)}
	done := make(chan error, 1)
	go func() { done <- s.openPendingModelCall() }()
	s.runControl.Interrupt(contracts.InterruptInfo{})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run interrupt did not stop backoff")
	}
	if protocol.openCount != 0 {
		t.Fatal("request started after run interrupt")
	}
	if !s.cancelSent {
		t.Fatal("missing cancellation terminal flow")
	}
}
