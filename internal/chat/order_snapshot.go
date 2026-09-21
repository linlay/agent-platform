package chat

// OrderSnapshot contains instance-wide preferences and all active pinned summaries.
// Runtime run state is enriched by the API after the storage lock is released.
type OrderSnapshot struct {
	Order OrderState
	Pins  PinnedState
	Chats []Summary
}

type OrderSnapshotStore interface {
	ChatOrderSnapshot() (OrderSnapshot, error)
}

func (s *FileStore) ChatOrderSnapshot() (OrderSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order := s.readChatOrderForListLocked()
	pins, err := s.readChatPinnedLocked()
	if err != nil {
		return OrderSnapshot{}, err
	}
	pinned := true
	items, err := s.listChatsWithPresentationLocked(ListOptions{Pinned: &pinned}, true, pins, order)
	if err != nil {
		return OrderSnapshot{}, err
	}
	// Derive the legacy ID projection from the same published list.
	pins.Order = make([]string, 0, len(items))
	for _, item := range items {
		pins.Order = append(pins.Order, item.ChatID)
	}
	return OrderSnapshot{Order: order, Pins: pins, Chats: items}, nil
}
