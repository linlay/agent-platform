package app

import (
	"context"
	"testing"
	"time"
)

type blockingScheduler struct {
	done context.Context
}

func (s blockingScheduler) Stop() context.Context {
	return s.done
}

func TestAppCloseReturnsWhenSchedulerStopTimesOut(t *testing.T) {
	previousTimeout := schedulerStopTimeout
	schedulerStopTimeout = 20 * time.Millisecond
	defer func() {
		schedulerStopTimeout = previousTimeout
	}()

	app := &App{
		scheduler: blockingScheduler{done: context.Background()},
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
