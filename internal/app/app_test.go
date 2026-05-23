package app

import (
	"context"
	"testing"
	"time"
)

type blockingAutomation struct {
	done context.Context
}

func (s blockingAutomation) Stop() context.Context {
	return s.done
}

func TestAppCloseReturnsWhenAutomationStopTimesOut(t *testing.T) {
	previousTimeout := automationStopTimeout
	automationStopTimeout = 20 * time.Millisecond
	defer func() {
		automationStopTimeout = previousTimeout
	}()

	app := &App{
		automation: blockingAutomation{done: context.Background()},
	}

	startedAt := time.Now()
	if err := app.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}
	elapsed := time.Since(startedAt)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("expected close to return promptly after timeout, took %s", elapsed)
	}
}
