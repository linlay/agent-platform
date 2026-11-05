package mailbox

import (
	"log"
	"time"
)

func (s *Service) retry(chatID string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil || s.retries[chatID] != nil {
		return
	}
	delay := s.retryDelay[chatID]
	if delay == 0 {
		delay = 5 * time.Second
	}
	s.retryDelay[chatID] = min(delay*2, 5*time.Minute)
	log.Printf("[mailbox] retry chat=%s in %s: %v", chatID, delay, cause)
	s.retries[chatID] = time.AfterFunc(delay, func() {
		s.mu.Lock()
		delete(s.retries, chatID)
		s.mu.Unlock()
		s.Kick(chatID)
	})
}

func (s *Service) resetRetry(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if timer := s.retries[chatID]; timer != nil {
		timer.Stop()
		delete(s.retries, chatID)
	}
	delete(s.retryDelay, chatID)
}
