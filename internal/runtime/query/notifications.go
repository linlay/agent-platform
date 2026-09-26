package query

func (s *Service) broadcast(eventType string, data map[string]any) {
	if s == nil || s.deps.Notifications == nil {
		return
	}
	s.deps.Notifications.Broadcast(eventType, data)
}
