package connectorauth

import (
	"agent-platform/internal/operationstate"
	"fmt"
	"path/filepath"
)

// Never persist authorization URLs, tokens, or upstream diagnostic messages.
func (m *Manager) persistAuthorization(s Session) error {
	s.URL = ""
	s.Message = ""
	return operationstate.Write(filepath.Join(m.sources.PersistentRoot(), "authorization-operations"), s.ConnectorID+"/"+s.ID, s)
}
func (m *Manager) persistedAuthorization(id, attempt string) (Session, error) {
	var s Session
	if attempt == "" {
		return s, fmt.Errorf("authorizationId is required")
	}
	err := operationstate.Read(filepath.Join(m.sources.PersistentRoot(), "authorization-operations"), id+"/"+attempt, &s)
	if err != nil {
		return s, err
	}
	if s.Status == "pending" || s.Status == "preparing" {
		s.Status = "interrupted"
	}
	return s, nil
}
