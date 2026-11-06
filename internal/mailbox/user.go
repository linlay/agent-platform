package mailbox

import (
	"context"

	"agent-platform/internal/runtime/controlscope"
)

// AcceptUser is called only after Runtime's ordinary user admission has passed.
func (s *Service) AcceptUser(ctx context.Context, m Mail, scope controlscope.Scope) (Receipt, error) {
	s.setScope(m.ID, scope)
	stored, created, err := s.Store.Accept(m)
	if !created {
		s.dropScope(m.ID)
	}
	if err != nil {
		return Receipt{}, err
	}
	if created {
		s.changed(stored)
	}
	// Drain under the same per-Chat lock used by background delivery. The caller
	// receives the accepted delivery decision, without joining the Run's stream.
	s.drain(stored.ToChatID)
	current, err := s.Store.Get(stored.ID)
	if err != nil {
		return Receipt{}, err
	}
	return receipt(current), nil
}
func (s *Service) drain(chatID string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	lock := s.chatLocks[chatID]
	if lock == nil {
		lock = &chatLock{}
		s.chatLocks[chatID] = lock
	}
	lock.refs++
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	lock.mu.Lock()
	err := s.dispatch(chatID)
	lock.mu.Unlock()
	s.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(s.chatLocks, chatID)
	}
	s.mu.Unlock()
	if err != nil {
		s.retry(chatID, err)
	}
}
