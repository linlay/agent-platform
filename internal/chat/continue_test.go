package chat

import "testing"

func TestCanContinueRequiresLatestFailedOrCanceledRun(t *testing.T) {
	for _, reason := range []string{"complete", "error", "cancel", "cancelled", "canceled", "interrupted", "unknown", ""} {
		t.Run(reason, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := store.EnsureChat("chat", "agent", "start"); err != nil {
				t.Fatal(err)
			}
			check := func(want bool) {
				t.Helper()
				summary, err := store.Summary("chat")
				if err != nil {
					t.Fatal(err)
				}
				if summary.CanContinue != want {
					t.Fatalf("canContinue=%v want %v", summary.CanContinue, want)
				}
				list, err := store.ListChats("", "")
				if err != nil || len(list) != 1 || list[0].CanContinue {
					t.Fatalf("list=%+v err=%v", list, err)
				}
			}
			check(false)
			if err := completeRunForTest(store, RunCompletion{ChatID: "chat", RunID: "r1", FinishReason: "cancel", UpdatedAtMillis: 1001}); err != nil {
				t.Fatal(err)
			}
			check(true)
			if err := completeRunForTest(store, RunCompletion{ChatID: "chat", RunID: "r2", FinishReason: reason, UpdatedAtMillis: 2001}); err != nil {
				t.Fatal(err)
			}
			want := reason == "error" || reason == "cancel" || reason == "cancelled" || reason == "canceled" || reason == "interrupted"
			check(want)
			// A newer unfinished run (including after a crash) must not inherit r2's eligibility.
			if err := store.OnRunStarted(RunStart{ChatID: "chat", RunID: "r3", StartedAtMillis: testEpochMillis(3000)}); err != nil {
				t.Fatal(err)
			}
			check(false)
		})
	}
}

func TestCanContinueBlocksAwaitingAndNewerRunAtSameTimestamp(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat", "agent", "start"); err != nil {
		t.Fatal(err)
	}
	if err := completeRunForTest(store, RunCompletion{ChatID: "chat", RunID: "z-old", FinishReason: "error", StartedAtMillis: testEpochMillis(1000), UpdatedAtMillis: testEpochMillis(1001)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingAwaiting("chat", PendingAwaiting{AwaitingID: "await", RunID: "z-old", Mode: "question", CreatedAt: testEpochMillis(1002)}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary("chat")
	if err != nil || summary.CanContinue {
		t.Fatalf("awaiting: %+v %v", summary, err)
	}
	if err := store.ClearPendingAwaiting("chat", "await"); err != nil {
		t.Fatal(err)
	}
	if err := store.OnRunStarted(RunStart{ChatID: "chat", RunID: "a-new", StartedAtMillis: testEpochMillis(1000)}); err != nil {
		t.Fatal(err)
	}
	summary, err = store.Summary("chat")
	if err != nil || summary.CanContinue {
		t.Fatalf("newer run: %+v %v", summary, err)
	}
}
