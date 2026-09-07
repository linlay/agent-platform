package query

import (
	"strings"
	"sync"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type DeferredAwaiting = runtimetypes.DeferredAwaiting

type DeferredAwaitingStore struct {
	contracts.AwaitingResolutionCoordinator
	mu    sync.Mutex
	items map[string]DeferredAwaiting
}

func NewDeferredAwaitingStore() *DeferredAwaitingStore {
	return &DeferredAwaitingStore{items: map[string]DeferredAwaiting{}}
}

func (s *DeferredAwaitingStore) Register(item DeferredAwaiting) {
	if s == nil {
		return
	}
	awaitingID := strings.TrimSpace(item.AwaitingID)
	if awaitingID == "" {
		return
	}
	s.mu.Lock()
	if previous, ok := s.items[awaitingID]; ok && previous.SupervisorCancel != nil {
		previous.SupervisorCancel()
	}
	s.items[awaitingID] = item
	s.mu.Unlock()
}

func (s *DeferredAwaitingStore) Lookup(awaitingID string) (DeferredAwaiting, bool) {
	if s == nil {
		return DeferredAwaiting{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[strings.TrimSpace(awaitingID)]
	return item, ok
}

func (s *DeferredAwaitingStore) Remove(awaitingID string) {
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
