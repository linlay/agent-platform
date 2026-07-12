package server

import (
	"testing"

	"agent-platform/internal/chat"
)

// startServerFixtureRun records the same explicit lifecycle start that the
// production query gate records before writing a run's JSONL records. Older
// server fixtures constructed completions directly; that would now invent a
// start time and is intentionally rejected by the public time contract.
func startServerFixtureRun(t testing.TB, store chat.Store, chatID, runID string, startedAt int64) {
	t.Helper()
	recorder, ok := store.(chat.RunStartRecorder)
	if !ok {
		return
	}
	if err := recorder.OnRunStarted(chat.RunStart{
		ChatID:          chatID,
		RunID:           runID,
		StartedAtMillis: startedAt,
	}); err != nil {
		t.Fatalf("record fixture run start %s: %v", runID, err)
	}
}

// completeServerFixtureRun is deliberately fixture-only. It makes the old
// compact test offsets explicit epoch-millisecond values and persists the
// required lifecycle row before the strict completion boundary.
func completeServerFixtureRun(t testing.TB, store chat.Store, completion chat.RunCompletion) error {
	t.Helper()
	if completion.UpdatedAtMillis < testEpochMillis {
		completion.UpdatedAtMillis = testEpochMillis + completion.UpdatedAtMillis
	}
	if completion.StartedAtMillis < testEpochMillis {
		completion.StartedAtMillis = completion.UpdatedAtMillis - 1
	}
	startServerFixtureRun(t, store, completion.ChatID, completion.RunID, completion.StartedAtMillis)
	return store.OnRunCompleted(completion)
}
