package query

import (
	"errors"
	"path/filepath"

	"agent-platform/internal/chat"
	"agent-platform/internal/interaction"
)

func (s *Service) RestoredInteractionPolicy(runID, mode string, query *chat.QueryLine) (*interaction.Config, error) {
	frozen, err := s.RunInteractionPolicies().Load(runID)
	if err == nil {
		return &frozen, nil
	}
	if !errors.Is(err, interaction.ErrMissing) {
		return nil, err
	}
	if query != nil {
		if raw, exists := query.Query["interactionConfig"]; exists {
			frozen, err := interaction.Parse(mode, raw)
			if err != nil {
				return nil, err
			}
			return &frozen, nil
		}
	}
	return nil, nil
}

func (s *Service) RunInteractionPolicies() interaction.Store {
	return interaction.Store{Root: filepath.Join(filepath.Dir(s.runControlScopes().Root), "run-interactions")}
}
