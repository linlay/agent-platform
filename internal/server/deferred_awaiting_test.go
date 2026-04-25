package server

import (
	"fmt"
	"sync"
	"testing"

	"agent-platform-runner-go/internal/chat"
)

func TestDeferredAwaitingStoreRegisterLookupRemove(t *testing.T) {
	store := NewDeferredAwaitingStore()
	item := DeferredAwaiting{
		ChatID:     "chat-1",
		AwaitingID: "await-1",
		RunID:      "run-1",
		Mode:       "question",
		CreatedAt:  123,
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
