package server

import (
	"fmt"

	"agent-platform/internal/models"
)

func (s *Server) validateLocalChatModelKey(modelKey string, providerBacked bool) error {
	if s.deps.Models == nil {
		return fmt.Errorf("model registry is not configured")
	}
	if providerBacked {
		_, _, err := s.deps.Models.Get(modelKey)
		return err
	}
	model, err := s.deps.Models.GetModel(modelKey)
	if err != nil {
		return err
	}
	if !models.IsChatModel(model) {
		return fmt.Errorf("model %s has type %s, want %s", model.Key, model.Type, models.ModelTypeChat)
	}
	return nil
}
