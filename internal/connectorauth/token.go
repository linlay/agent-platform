package connectorauth

import (
	"context"
	"fmt"
	"os"

	"agent-platform/internal/connector"
)

// TokenValues reads deployment credentials only; packages never supply values.
func TokenValues(pkg connector.Package) (map[string]string, bool, error) {
	path, err := connector.CredentialsPath(pkg.PersistentRoot(), pkg.ID)
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
	unlock, err := lockCredentials(ctx, m.sources.PersistentRoot(), id)
	if err != nil {
		return Session{}, err
	}
	path, err := connector.CredentialsPath(m.sources.PersistentRoot(), id)
	if err == nil {
		err = savePrivateJSON(path, values)
	}
	unlock()
	if err != nil {
		return Session{}, fmt.Errorf("save connector credentials failed")
	}
	if m.reload != nil {
		if err := m.reload(ctx, id); err != nil {
			return Session{}, err
		}
	}
	return Session{ConnectorID: id, Status: "authorized"}, nil
}
