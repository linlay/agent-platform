package adminsource

import (
	"context"
	"fmt"

	"agent-platform/internal/catalog"
)

type AgentConnectorEditor interface {
	ReadAgentConnectors(key string) ([]string, error)
	PrepareAgentConnector(key, id string, enabled bool) (catalog.AgentConnectorCandidate, error)
	WriteEditableAgentSource(key, content, baseSHA256 string) (catalog.EditableAgentSourceFile, error)
}

type AgentConnectorReloadError struct{ Cause error }

func (e *AgentConnectorReloadError) Error() string { return e.Cause.Error() }
func (e *AgentConnectorReloadError) Unwrap() error { return e.Cause }

// SetAgentConnector serializes with the other Agent editors, patches the latest
// source and reloads through the normal catalog publisher. A failed reload
// restores the original source only if no other writer has changed it.
func (s *Service) SetAgentConnector(ctx context.Context, editor AgentConnectorEditor, key, id string, enabled bool, reload func(context.Context) error) ([]string, error) {
	unlock := s.LockAgentMutation()
	defer unlock()
	candidate, err := editor.PrepareAgentConnector(key, id, enabled)
	if err != nil {
		return nil, err
	}
	if candidate.Content == candidate.Source.Content {
		return candidate.ConnectorIDs, nil
	}
	written, err := editor.WriteEditableAgentSource(key, candidate.Content, candidate.Source.SHA256)
	if err != nil {
		return nil, err
	}
	// Client cancellation must not leave persisted source without publication.
	ctx = context.WithoutCancel(ctx)
	if err := reload(ctx); err != nil {
		_, rollbackErr := editor.WriteEditableAgentSource(key, candidate.Source.Content, written.SHA256)
		if rollbackErr != nil {
			return nil, &AgentConnectorReloadError{fmt.Errorf("reload agent: %w; restore source: %v", err, rollbackErr)}
		}
		if recoveryErr := reload(ctx); recoveryErr != nil {
			return nil, &AgentConnectorReloadError{fmt.Errorf("reload agent: %w; reload restored source: %v", err, recoveryErr)}
		}
		return nil, &AgentConnectorReloadError{fmt.Errorf("reload agent: %w; original source restored", err)}
	}
	return candidate.ConnectorIDs, nil
}
