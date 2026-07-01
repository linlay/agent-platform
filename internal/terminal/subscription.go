package terminal

import "sync/atomic"

type Subscription struct {
	session *Session
	id      int64
	events  <-chan Event
}

var nextSubscriptionID atomic.Int64

func (s *Session) Finished() bool {
	if s == nil {
		return true
	}
	return s.finished.Load()
}

func (s *Session) Subscribe(includeReplay bool) *Subscription {
	if s == nil {
		events := make(chan Event)
		close(events)
		return &Subscription{events: events}
	}
	ch := make(chan Event, 128)
	id := nextSubscriptionID.Add(1)
	s.mu.Lock()
	if s.finished.Load() {
		close(ch)
		s.mu.Unlock()
		return &Subscription{id: id, events: ch}
	}
	if s.subscribers == nil {
		s.subscribers = map[int64]chan Event{}
	}
	s.subscribers[id] = ch
	if includeReplay && len(s.replay) > 0 {
		ch <- s.event(Event{
			Type:   EventOutput,
			Data:   string(s.replay),
			Replay: true,
		})
	}
	s.mu.Unlock()
	return &Subscription{session: s, id: id, events: ch}
}

func (s *Session) removeSubscriber(id int64) {
	if s == nil || id <= 0 {
		return
	}
	s.mu.Lock()
	if ch, ok := s.subscribers[id]; ok {
		delete(s.subscribers, id)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Subscription) Events() <-chan Event {
	if s == nil {
		events := make(chan Event)
		close(events)
		return events
	}
	return s.events
}

func (s *Subscription) Close() {
	if s == nil || s.session == nil {
		return
	}
	s.session.removeSubscriber(s.id)
}
