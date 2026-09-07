package contracts

import (
	"sync"
	"testing"
	"time"
)

func TestDeferredAwaitingResolutionLockSerializesAndReleasesKeys(t *testing.T) {
	store := &AwaitingResolutionCoordinator{}
	unlock := store.LockResolution("chat", "run", "await")
	independent := make(chan struct{})
	go func() { release := store.LockResolution("chat", "other-run", "await"); release(); close(independent) }()
	select {
	case <-independent:
	case <-time.After(time.Second):
		t.Fatal("different run's resolution blocked")
	}
	unlock()
	unlock()
	var wg sync.WaitGroup
	count := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				release := store.LockResolution("chat", "run", "await")
				count++
				release()
			}
		}()
	}
	wg.Wait()
	if count != 800 {
		t.Fatalf("lost updates: %d", count)
	}
	if len(store.resolutions) != 0 {
		t.Fatalf("resolution locks leaked: %d", len(store.resolutions))
	}
}
