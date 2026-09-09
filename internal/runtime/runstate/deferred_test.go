package runstate

import (
	"fmt"
	"sync"
	"testing"

	"agent-platform/internal/chat"
)

func TestDeferredAwaitingStoreRegisterLookupRemove(t *testing.T) {
	store := NewDeferredAwaitingStore()
	item := DeferredAwaiting{
		ChatID:     "chat-1",
		AwaitingID: "await-1",
		RunID:      "run-1",
		Mode:       "question",
		CreatedAt:  1_700_000_000_123,
		Ask: &chat.PersistedAwaitingAsk{
			AwaitingID: "await-1",
			RunID:      "run-1",
			Mode:       "question",
			Payload:    map[string]any{"type": "awaiting.ask"},
		},
	}

	store.Register(item)
	got, ok := store.Lookup("await-1")
	if !ok || got.ChatID != item.ChatID || got.RunID != item.RunID {
		t.Fatalf("unexpected lookup result %#v ok=%v", got, ok)
	}

	store.Remove("await-1")
	if _, ok := store.Lookup("await-1"); ok {
		t.Fatal("expected awaiting to be removed")
	}
}

func TestDeferredAwaitingStoreConcurrentAccess(t *testing.T) {
	store := NewDeferredAwaitingStore()
	var wg sync.WaitGroup

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			awaitingID := fmt.Sprintf("await-%d", i)
			store.Register(DeferredAwaiting{AwaitingID: awaitingID, RunID: fmt.Sprintf("run-%d", i)})
			if got, ok := store.Lookup(awaitingID); !ok || got.RunID == "" {
				t.Errorf("lookup failed for %s: %#v ok=%v", awaitingID, got, ok)
			}
			store.Remove(awaitingID)
		}(i)
	}

	wg.Wait()
}

func TestDeferredAwaitingStoreCancelsSupersededAndRemovedSupervisors(t *testing.T) {
	store := NewDeferredAwaitingStore()
	previousCanceled, currentCanceled := 0, 0
	store.Register(DeferredAwaiting{AwaitingID: " await-1 ", SupervisorCancel: func() { previousCanceled++ }})
	store.Register(DeferredAwaiting{AwaitingID: "await-1", RunID: "run-2", SupervisorCancel: func() { currentCanceled++ }})
	if previousCanceled != 1 || currentCanceled != 0 {
		t.Fatalf("replacement canceled wrong supervisor: previous=%d current=%d", previousCanceled, currentCanceled)
	}
	if got, ok := store.Lookup(" await-1 "); !ok || got.RunID != "run-2" {
		t.Fatalf("replacement missing: %#v ok=%v", got, ok)
	}
	store.Remove(" await-1 ")
	store.Remove("await-1")
	if previousCanceled != 1 || currentCanceled != 1 {
		t.Fatalf("removal must cancel the current supervisor once: previous=%d current=%d", previousCanceled, currentCanceled)
	}
}
