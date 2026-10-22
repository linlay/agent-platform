package adminsource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

type ConnectorUsageReader interface {
	ConnectorUsers(id string) ([]string, error)
}

var ErrConnectorDetach = errors.New("cannot remove connector mounts")

type ConnectorInUseError struct{ AgentKeys []string }

func (e *ConnectorInUseError) Error() string {
	return "connector is still used by agents: " + strings.Join(e.AgentKeys, ", ") + "; end the runs or terminals using it before deleting"
}

func (s *Service) DeleteConnector(ctx context.Context, sources connector.Sources, reader ConnectorUsageReader, id string, reload func(context.Context) error, coordinate ...func(context.Context, func(context.Context) error) error) error {
	unlock := s.LockAgentMutation()
	defer unlock()
	mutate := func(ctx context.Context) error {
		return s.deleteConnectorLocked(ctx, sources, reader, id, reload)
	}

	// Keep the established Agent -> catalog -> connector lock order. Acquiring
	// the catalog guard first could deadlock with an Agent edit that reloads.
	if len(coordinate) > 0 && coordinate[0] != nil {
		return coordinate[0](ctx, mutate)
	}
	return mutate(ctx)
}

// ConnectorDeletionEditor separates saved mounts from active execution leases.
type ConnectorDeletionEditor interface {
	ConnectorUsageReader
	ConfiguredConnectorUsers(string) ([]string, error)
	ActiveConnectorUsers(string) ([]string, error)
	BeginConnectorDeletion(string) (func(), []string, error)
	PrepareConnectorDetach(string) (*catalog.ConnectorDetachMutation, error)
}

// Caller holds the Agent mutation lock and catalog coordinator. The execution
// gate is independent of catalog reload, which must still take executionMu.
func (s *Service) deleteConnectorLocked(ctx context.Context, sources connector.Sources, reader ConnectorUsageReader, id string, reload func(context.Context) error) error {
	if connector.IsBuiltin(id) {
		return connector.ErrBuiltinReadOnly
	}
	if !connector.ValidID(id) {
		return fmt.Errorf("invalid connector deletion target")
	}
	editor, ok := reader.(ConnectorDeletionEditor)
	if !ok {
		return fmt.Errorf("connector deletion editor is not configured")
	}
	release, users, err := editor.BeginConnectorDeletion(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return &ConnectorInUseError{AgentKeys: users}
	}
	defer release()
	mutation, err := editor.PrepareConnectorDetach(id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectorDetach, err)
	}
	if err := mutation.Apply(); err != nil {
		return fmt.Errorf("%w: %w", ErrConnectorDetach, err)
	}
	ctx = context.WithoutCancel(ctx)
	err = connector.DeletePackage(ctx, sources, id, func(id string) error {
		users, err := editor.ConfiguredConnectorUsers(id)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("connector mounts changed during deletion: %v", users)
		}
		return nil
	}, func() error {
		if reload != nil {
			return reload(ctx)
		}
		return nil
	})
	if err == nil || errors.Is(err, connector.ErrDeleteCleanup) {
		return errors.Join(err, mutation.Commit())
	}
	rollbackErr := mutation.Rollback()
	var reloadErr error
	if reload != nil {
		reloadErr = reload(ctx)
	}
	return errors.Join(err, rollbackErr, reloadErr)
}
