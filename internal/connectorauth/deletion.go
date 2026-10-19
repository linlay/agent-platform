package connectorauth

import (
	"context"
	"fmt"

	"agent-platform/internal/connector"
)

// BeginDelete stops local writers before the caller takes Agent/catalog locks.
// Login and preparation completion may reload the catalog, so waiting for them
// inside that transaction would deadlock. Cross-process writers are fenced by
// DeletePackage's operation and stable state locks.
func (m *Manager) BeginDelete(ctx context.Context, id string) (func(), error) {
	if connector.IsBuiltin(id) {
		return nil, connector.ErrBuiltinReadOnly
	}
	if !connector.ValidID(id) {
		return nil, fmt.Errorf("invalid connector deletion target")
	}
	if err := m.beginDisconnect(id); err != nil {
		return nil, fmt.Errorf("%w: %v", connector.ErrBusy, err)
	}
	m.retire(id)
	release := func() {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		m.endDisconnect(id)
	}
	if err := m.Cancel(id); err != nil {
		release()
		return nil, fmt.Errorf("%w: %v", connector.ErrBusy, err)
	}
	if err := m.cancelTokenValidation(ctx, id); err != nil {
		release()
		return nil, fmt.Errorf("%w: %v", connector.ErrBusy, err)
	}
	if err := m.CancelPreparation(id); err != nil {
		release()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// Discovery can finish after a package was deleted and re-imported. The same
// durable generation check as token exchange also protects its metadata cache.
func saveLoginDiscovery(ctx context.Context, pkg connector.Package, generation, path string, value any) error {
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readAuthState(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || state.Generation != generation {
		return fmt.Errorf("connector authorization superseded")
	}
	return savePrivateJSON(path, value)
}
