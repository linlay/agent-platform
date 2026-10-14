package connectorauth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"agent-platform/internal/connector"
)

// TokenValues reads deployment credentials only; packages never supply values.
func TokenValues(pkg connector.Package) (map[string]string, bool, error) {
	path, err := connector.CredentialsPath(pkg.CredentialRoot(), pkg.ID)
	if err != nil {
		return nil, false, fmt.Errorf("connector credential store is invalid")
	}
	var values map[string]string
	if err := connector.ReadJSON(path, &values); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("connector credential store is invalid")
	}
	if err := connector.ValidateTokenValues(pkg.Manifest, values); err != nil {
		return nil, false, nil
	}
	return values, true, nil
}

// ReadTokenCredentials reads the active account and its revision under the
// credential lock, so an execution never observes a partially committed account.
func ReadTokenCredentials(ctx context.Context, root, id string) (map[string]string, string, error) {
	unlock, err := lockCredentials(ctx, root, id)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	path, err := connector.CredentialsPath(root, id)
	if err != nil {
		return nil, "", err
	}
	var values map[string]string
	if err := readPrivateJSON(path, &values); err != nil {
		return nil, "", err
	}
	state, err := readAuthState(root, id)
	if err != nil {
		return nil, "", err
	}
	return values, state.Generation + ":" + state.Revision, nil
}

// SetToken keeps failed candidates out of the active credential file.
func (m *Manager) SetToken(ctx context.Context, id string, values map[string]string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.AuthMode != connector.AuthToken {
		return Session{}, fmt.Errorf("only token connectors accept manual credentials")
	}
	if err = connector.ValidateTokenValues(pkg.Manifest, values); err != nil {
		return Session{}, err
	}
	candidate := make(map[string]string, len(values))
	for k, v := range values {
		candidate[k] = v
	}
	return m.tokenOperation(ctx, pkg, candidate, false)
}
func (m *Manager) checkToken(ctx context.Context, pkg connector.Package) (Session, error) {
	return m.tokenOperation(ctx, pkg, nil, true)
}
func (m *Manager) tokenOperation(ctx context.Context, pkg connector.Package, values map[string]string, check bool) (Session, error) {
	requestCtx := ctx
	ctx, generation, finish, err := m.beginTokenValidation(ctx, pkg.ID)
	if err != nil {
		return Session{}, err
	}
	defer finish()
	pending, ready := false, true
	inputs := func() error {
		if !check {
			return nil
		}
		var err error
		values, pending, err = pendingTokenValues(pkg)
		if err != nil || pending {
			return err
		}
		values, ready, err = TokenValues(pkg)
		return err
	}
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return Session{}, err
	}
	err = inputs()
	before, stateErr := readAuthState(pkg.PersistentRoot(), pkg.ID)
	unlock()
	if err != nil {
		return Session{}, err
	}
	if stateErr != nil {
		return Session{}, ErrCredentialStoreUnavailable
	}
	if !ready {
		return Session{ConnectorID: pkg.ID, Status: "unauthorized", AuthBrowser: pkg.AuthorizationBrowser()}, nil
	}
	// Validation may take tens of seconds. Executions keep reading the active
	// account meanwhile; the commit below re-locks and rejects a changed state.
	status, probeErr := m.validateTokenCredentials(ctx, pkg, values)
	unlock, err = lockCredentials(requestCtx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return Session{}, err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	validated, validatedPending := values, pending
	after, stateErr := readAuthState(pkg.PersistentRoot(), pkg.ID)
	if err := inputs(); err != nil || stateErr != nil || after != before || pending != validatedPending || !maps.Equal(values, validated) {
		return Session{}, fmt.Errorf("connector credential operation superseded")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if requestCtx.Err() != nil || errors.Is(ctx.Err(), context.Canceled) || m.epochs[pkg.ID] != generation || m.disconnecting[pkg.ID] {
		return Session{}, fmt.Errorf("connector credential operation superseded")
	}
	result := Session{ConnectorID: pkg.ID, Status: status, AuthBrowser: pkg.AuthorizationBrowser()}
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return Session{}, err
	}
	if probeErr != nil {
		if errors.Is(probeErr, ErrTokenRejected) {
			if check && !pending {
				if err = saveVerification(pkg, values, "unauthorized"); err != nil {
					return Session{}, err
				}
			}
			return Session{}, ErrTokenRejected
		}
		if check && !pending {
			return Session{ConnectorID: pkg.ID, Status: "failed", AuthBrowser: pkg.AuthorizationBrowser(), Message: "Credential check unavailable; saved credentials were preserved"}, nil
		}
		if err = savePrivateJSON(filepath.Join(dir, "pending-credentials.json"), values); err != nil {
			return Session{}, fmt.Errorf("save pending credentials failed")
		}
		// A candidate awaiting a real check cannot establish a new binding or
		// change an existing one. In particular, an offline first submission
		// must not become configured before the candidate is accepted.
		result.Status = "pending_verification"
		result.PendingVerification = true
		result.Message = "Credentials saved for later verification; active credentials were preserved"
		return result, nil
	}
	if err = m.commitTokenCredentials(pkg, values, status); err != nil {
		return Session{}, err
	}
	// Reload may reconnect components that read credentials under the same lock.
	unlock()
	locked = false
	// Callbacks run after releasing the manager lock, as they may inspect current state.
	if m.reload != nil {
		m.mu.Unlock()
		err = m.reload(requestCtx, pkg.ID)
		m.mu.Lock()
		if err != nil {
			return Session{}, fmt.Errorf("credentials saved; connector runtime refresh failed")
		}
	}
	if status == "configured" {
		result.Message = tokenConfiguredMessage
	}
	return result, nil
}
