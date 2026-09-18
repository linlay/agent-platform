package adminsource

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/connector"
)

type ConnectorUsageReader interface {
	ConnectorUsers(id string) ([]string, error)
}

type ConnectorInUseError struct{ AgentKeys []string }

func (e *ConnectorInUseError) Error() string {
	return "connector is still used by agents: " + strings.Join(e.AgentKeys, ", ") + "; detach it and wait for active runtimes to finish before deleting"
}

func (s *Service) DeleteConnector(ctx context.Context, sources connector.Sources, reader ConnectorUsageReader, id string, reload func(context.Context) error, coordinate ...func(context.Context, func(context.Context) error) error) error {
	unlock := s.LockAgentMutation()
	defer unlock()
	mutate := func(ctx context.Context) error {
		return connector.DeletePackage(ctx, sources, id, func(id string) error {
			if reader == nil {
				return fmt.Errorf("connector usage reader is not configured")
			}
			keys, err := reader.ConnectorUsers(id)
			if err != nil {
				return err
			}
			if len(keys) > 0 {
				return &ConnectorInUseError{AgentKeys: keys}
			}
			return nil
		}, func() error {
			return reload(context.WithoutCancel(ctx))
		})
	}
	// Keep the established Agent -> catalog -> connector lock order. Acquiring
	// the catalog guard first could deadlock with an Agent edit that reloads.
	if len(coordinate) > 0 && coordinate[0] != nil {
		return coordinate[0](ctx, mutate)
	}
	return mutate(ctx)
}
