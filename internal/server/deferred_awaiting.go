package server

import (
	"strings"
	"sync"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type DeferredAwaiting = runtimetypes.DeferredAwaiting

type DeferredAwaitingStore interface {
	Register(DeferredAwaiting)
	Lookup(string) (DeferredAwaiting, bool)
	Remove(string)
	LockResolution(chatID, runID, awaitingID string) func()
}

// localDeferredAwaitingStore is retained only for direct Server construction.
// app.New injects the query runtime's continuation store.
type localDeferredAwaitingStore struct {
	contracts.AwaitingResolutionCoordinator
	mu    sync.Mutex
	items map[string]DeferredAwaiting
}

func newLocalDeferredAwaitingStore() *localDeferredAwaitingStore {
	return &localDeferredAwaitingStore{items: map[string]DeferredAwaiting{}}
}

func (s *localDeferredAwaitingStore) Register(item DeferredAwaiting) {
	if s == nil || strings.TrimSpace(item.AwaitingID) == "" {
		return
	}
	s.mu.Lock()
	if previous, ok := s.items[strings.TrimSpace(item.AwaitingID)]; ok && previous.SupervisorCancel != nil {
		previous.SupervisorCancel()
	}
	s.items[strings.TrimSpace(item.AwaitingID)] = item
	s.mu.Unlock()
}

func (s *localDeferredAwaitingStore) Lookup(awaitingID string) (DeferredAwaiting, bool) {
	if s == nil {
		return DeferredAwaiting{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[strings.TrimSpace(awaitingID)]
	return item, ok
}

func (s *localDeferredAwaitingStore) Remove(awaitingID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	key := strings.TrimSpace(awaitingID)
	if item, ok := s.items[key]; ok && item.SupervisorCancel != nil {
		item.SupervisorCancel()
	}
	delete(s.items, key)
	s.mu.Unlock()
}
