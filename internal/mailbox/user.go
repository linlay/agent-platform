package mailbox

import (
	"agent-platform/internal/contracts/queryinput"
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
	if err := s.withDeliveryLock(chatID, func() error { return s.dispatch(chatID) }); err != nil {
		s.retry(chatID, err)
	}
}
func (s *Service) withDeliveryLock(chatID string, fn func() error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return context.Canceled
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
	err := fn()
	lock.mu.Unlock()
	s.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(s.chatLocks, chatID)
	}
	s.mu.Unlock()
	return err
}

// CancelUser serializes against dispatch. A delivered letter is never declared
// withdrawn; the caller then interrupts its exact started Run instead.
func (s *Service) CancelUser(id string) (Mail, error) {
	m, err := s.Store.Get(id)
	if err != nil {
		return m, err
	}
	err = s.withDeliveryLock(m.ToChatID, func() error {
		var e error
		m, e = s.change(id, func(row *Mail) error {
			if !queryinput.UserMailOrigin(row.OriginType) {
				return failure("mail_not_owned", "only user/automation messages may be cancelled here")
			}
			if row.Status == Queued || row.Status == Accepting || row.Status == Suspended && row.ConsumedAt == 0 && row.Delivery == "" {
				row.Status = Withdrawn
				row.TerminalAt = now()
			}
			return nil
		})
		return e
	})
	return m, err
}
