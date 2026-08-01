package server

import (
	"strings"
	"sync"

	"agent-platform/internal/chat"
)

type DeferredAwaiting struct {
	ChatID     string
	AwaitingID string
	RunID      string
	Mode       string
	CreatedAt  int64
	Ask        *chat.PersistedAwaitingAsk
	// TerminalCode keeps a restart-time tombstone addressable by awaitingId
	// even after the chat's pending summary has been cleared.
	TerminalCode string
}

type DeferredAwaitingStore struct {
	mu    sync.Mutex
	items map[string]DeferredAwaiting
}

func NewDeferredAwaitingStore() *DeferredAwaitingStore {
	return &DeferredAwaitingStore{
		items: map[string]DeferredAwaiting{},
	}
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
	delete(s.items, strings.TrimSpace(awaitingID))
	s.mu.Unlock()
}
