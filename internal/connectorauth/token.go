package connectorauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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

func (m *Manager) SetToken(ctx context.Context, id string, values map[string]string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.Builtin {
		return Session{}, connector.ErrBuiltinReadOnly
	}
	if pkg.AuthMode != connector.AuthToken {
		return Session{}, fmt.Errorf("only token connectors accept manual credentials")
	}
	if err := connector.ValidateTokenValues(pkg.Manifest, values); err != nil {
		return Session{}, err
	}
	// Never retain the caller's mutable map across a network probe.
	candidateValues := make(map[string]string, len(values))
	for key, value := range values {
		candidateValues[key] = value
	}
	ctx, generation, finish, err := m.beginTokenValidation(ctx, id)
	if err != nil {
		return Session{}, err
	}
	defer finish()
	unlock, err := lockCredentials(ctx, m.sources.PersistentRoot(), id)
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	status, err := m.validateTokenCredentials(ctx, pkg, candidateValues)
	if err != nil {
		return Session{}, err
	}
	// Disconnect invalidates the generation under the same lock as this commit.
	// The credential file rename atomically replaces the complete validated set.
	m.mu.Lock()
	if ctx.Err() != nil || m.epochs[id] != generation || m.disconnecting[id] || m.loggingOut[id] {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("connector credential operation superseded")
	}
	path, err := connector.CredentialsPath(m.sources.PersistentRoot(), id)
	var previous []byte
	if err == nil {
		previous, err = os.ReadFile(path)
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err == nil {
		err = savePrivateJSON(path, candidateValues)
	}
	if err == nil {
		err = m.markBound(pkg)
		if err != nil {
			// A failed preference commit must not discard an existing valid key.
			if previous == nil {
				_ = os.Remove(path)
			} else {
				_ = savePrivateJSON(path, json.RawMessage(previous))
			}
		}
	}
	if err == nil {
		err = changeAuthState(m.sources.PersistentRoot(), id, false)
	}
	m.mu.Unlock()
	if err != nil {
		return Session{}, fmt.Errorf("save connector credentials failed")
	}
	if m.reload != nil {
		if err := m.reload(ctx, id); err != nil {
			return Session{}, fmt.Errorf("credentials saved; connector runtime refresh failed")
		}
	}
	result := Session{ConnectorID: id, Status: status, AuthBrowser: pkg.AuthorizationBrowser()}
	if status == "configured" {
		result.Message = "Credentials configured; this connector has no independent verification command"
	}
	return result, nil
}
