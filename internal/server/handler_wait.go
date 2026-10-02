package server

// AuthorizationWaitStatus exposes only an attempt's identity and outcome.
func (s *Server) AuthorizationWaitStatus(connectorID, authorizationID string) (string, error) {
	session, err := s.connectorAuth.SessionStatus(connectorID, authorizationID)
	return session.Status, err
}
