package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	pending := false
	if check {
		values, pending, err = pendingTokenValues(pkg)
		if err != nil {
			return Session{}, err
		}
		if !pending {
			var ready bool
			values, ready, err = TokenValues(pkg)
			if err != nil {
				return Session{}, err
			}
			if !ready {
				return Session{ConnectorID: pkg.ID, Status: "unauthorized", AuthBrowser: pkg.AuthorizationBrowser()}, nil
			}
		}
	}
	status, probeErr := m.validateTokenCredentials(ctx, pkg, values)
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
		if _, err = pkg.SetConfigured(true); err != nil {
			return Session{}, err
		}
		result.Status = "pending_verification"
		result.PendingVerification = true
		result.Message = "Credentials saved for later verification; active credentials were preserved"
		return result, nil
	}
	path, err := connector.CredentialsPath(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return Session{}, err
	}
	var previous json.RawMessage
	if data, readErr := os.ReadFile(path); readErr == nil {
		previous = data
	} else if !os.IsNotExist(readErr) {
		return Session{}, fmt.Errorf("credential store unavailable")
	}
	if err = savePrivateJSON(path, values); err != nil {
		return Session{}, fmt.Errorf("save connector credentials failed")
	}
	if err = m.markConfigured(pkg); err != nil {
		if previous == nil {
			_ = os.Remove(path)
		} else {
			_ = savePrivateJSON(path, previous)
		}
		return Session{}, err
	}
	if err = changeAuthState(pkg.PersistentRoot(), pkg.ID, false); err != nil {
		return Session{}, err
	}
	if err = saveVerification(pkg, values, status); err != nil {
		return Session{}, err
	}
	if err = os.Remove(filepath.Join(dir, "pending-credentials.json")); err != nil && !os.IsNotExist(err) {
		return Session{}, fmt.Errorf("clear pending credentials failed")
	}
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
		result.Message = "Credentials configured; this connector has no independent verification command"
	}
	return result, nil
}
