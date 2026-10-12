package query

import "agent-platform/internal/interaction"

// FrozenInteractionPolicy returns the interactionConfig a Run was admitted
// with while this process still holds that Run. Nothing is persisted: restart
// cancels old Runs, and a new query uses the Agent's current definition.
func (s *Service) FrozenInteractionPolicy(runID string) *interaction.Config {
	reader, ok := s.deps.Runs.(interface {
		RunInteractionConfig(string) (interaction.Config, bool)
	})
	if !ok {
		return nil
	}
	if frozen, found := reader.RunInteractionConfig(runID); found {
		return &frozen
	}
	return nil
}
