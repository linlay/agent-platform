package connectorauth

import (
	"fmt"
	"time"
)

// Revision changes after login/refresh/logout and is suitable for fencing a
// result. It is not an authentication token and is never a credential.
func (m *Manager) Revision(id string) (string, error) {
	s, err := readAuthState(m.sources.PersistentRoot(), id)
	return s.Generation + ":" + s.Revision, err
}

// SessionStatus addresses one exact attempt. Unlike StatusComponent, it does not
// invoke CLI status or silently return a replacement session.
func (m *Manager) SessionStatus(id, sessionID string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if sessionID == "" || s == nil || s.ID != sessionID {
		return Session{}, fmt.Errorf("authorization session unavailable")
	}
	result := s.Session
	if !time.Now().Before(s.ExpiresAt) && (result.Status == "pending" || result.Status == "preparing") {
		result.Status = "expired"
		result.URL = ""
		s.cancel()
	}
	return result, nil
}
