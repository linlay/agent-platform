package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type Capabilities struct {
	CanConnect    bool               `json:"canConnect"`
	CanDisconnect bool               `json:"canDisconnect"`
	CanEnable     bool               `json:"canEnable"`
	AuthMode      connector.AuthMode `json:"authMode"`
	AuthBrowser   string             `json:"authBrowser"`
	HasCLI        bool               `json:"hasCli"`
	HasMCP        bool               `json:"hasMcp"`
}
type Connection struct {
	connector.ConnectionState
	Readiness      string       `json:"readiness"`
	Authentication Session      `json:"authentication"`
	Capabilities   Capabilities `json:"capabilities"`
	Preparation    *Preparation `json:"preparation,omitempty"`
}

func (m *Manager) Connection(ctx context.Context, id string) (Connection, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Connection{}, err
	}
	state, err := pkg.ReadConnection()
	if err != nil {
		return Connection{}, err
	}
	c := Connection{ConnectionState: state, Readiness: "not_connected", Capabilities: Capabilities{CanConnect: !pkg.Builtin, CanDisconnect: !pkg.Builtin, CanEnable: !pkg.Builtin, AuthMode: pkg.AuthMode, AuthBrowser: pkg.AuthorizationBrowser(), HasCLI: pkg.CLI != nil, HasMCP: len(pkg.MCP) > 0}}
	if pkg.CLI != nil && !pkg.Builtin {
		prep, err := m.PreparationStatus(id)
		if err != nil {
			prep = Preparation{ConnectorID: id, Status: "failed", Message: "Connector preparation unavailable"}
		}
		prep.Diagnostic = ""
		c.Preparation = &prep
		if prep.Status != "ready" {
			c.Readiness = "preparing"
			c.Authentication = Session{ConnectorID: id, Status: "setup_required", AuthBrowser: pkg.AuthorizationBrowser()}
			return c, nil
		}
	}
	if pkg.Builtin {
		c.Authentication = Session{ConnectorID: id, Status: "delegated", AuthBrowser: pkg.AuthorizationBrowser()}
		c.Readiness = "ready"
		return c, nil
	}
	auth, err := m.Status(ctx, id)
	if err != nil {
		c.Authentication = Session{ConnectorID: id, Status: "failed", Message: "Authorization status unavailable"}
		c.Readiness = "unavailable"
		return c, nil
	}
	c.Authentication = auth
	if !state.Bound {
		return c, nil
	}
	if auth.Status == "unauthorized" {
		c.Readiness = "authorization_required"
		return c, nil
	}
	if !state.Enabled {
		c.Readiness = "disabled"
		return c, nil
	}
	switch auth.Status {
	case "authorized", "delegated", "configured":
		c.Readiness = "ready"
	case "preparing", "pending":
		c.Readiness = "preparing"
	case "unauthorized":
		c.Readiness = "authorization_required"
	default:
		c.Readiness = "unavailable"
	}
	return c, nil
}
func (m *Manager) SetEnabled(ctx context.Context, id string, enabled bool) (Connection, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Connection{}, err
	}
	if _, err = pkg.UpdateConnection(nil, &enabled); err != nil {
		return Connection{}, err
	}
	return m.Connection(ctx, id)
}

type DisconnectResult struct {
	ConnectorID      string   `json:"connectorId"`
	Bound            bool     `json:"bound"`
	Enabled          bool     `json:"enabled"`
	RemoteRevocation string   `json:"remote_revocation"`
	Warnings         []string `json:"warnings,omitempty"`
}

func (m *Manager) Disconnect(ctx context.Context, id string) (DisconnectResult, error) {
	result := DisconnectResult{ConnectorID: id, RemoteRevocation: "unsupported"}
	pkg, err := m.sources.Load(id)
	if err != nil {
		return result, err
	}
	if pkg.Builtin {
		return result, connector.ErrBuiltinReadOnly
	}
	if err = m.beginDisconnect(id); err != nil {
		return result, err
	}
	defer m.endDisconnect(id)
	m.retire(id)
	no := false
	if _, err = pkg.UpdateConnection(&no, &no); err != nil {
		return result, err
	}
	if err = m.cancelBusiness(id); err != nil {
		return result, err
	}
	if m.disconnectHandler != nil {
		if err = m.disconnectHandler(context.WithoutCancel(ctx), m.sources.Owner, id); err != nil {
			return result, err
		}
	}
	// Prevent new calls before canceling. Cancel waits for this owner's callback,
	// so it cannot persist credentials or restore a binding after disconnect.
	if err = m.Cancel(id); err != nil {
		return result, err
	}
	if pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP {
		var cleanupErr error
		result.RemoteRevocation, err, cleanupErr = m.revokeAndClearOAuth(ctx, pkg)
		if cleanupErr != nil {
			return result, fmt.Errorf("connector private login cleanup failed")
		}
		if err != nil {
			result.Warnings = append(result.Warnings, "Third-party revocation failed; local credentials were cleared")
		}
	} else if pkg.AuthMode != connector.AuthOneID && !(pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI()) {
		if err = m.Logout(ctx, id); err != nil {
			result.Warnings = append(result.Warnings, "Third-party sign-out failed; local credentials were cleared")
		}
	}
	dir, err := pkg.UserStateDir()
	if err != nil {
		return result, err
	}
	// Keep preference history and shared installations; clear only private login environment.
	for _, name := range []string{"home", "config", "cache", "data", "state", "tmp", "credentials.json", "oauth.json", "oauth-resources"} {
		if err = os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return result, fmt.Errorf("connector private login cleanup failed")
		}
	}
	_, err = pkg.UpdateConnection(&no, &no)
	return result, err
}

func (m *Manager) Connect(id string) (Session, error) {
	generation := m.epoch(id)
	if m.changing(id) {
		return Session{}, fmt.Errorf("connector disconnect is in progress")
	}
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.Builtin {
		return Session{}, connector.ErrBuiltinReadOnly
	}
	if pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI() || pkg.AuthMode == connector.AuthOneID {
		status, err := m.Status(m.ctx, id)
		if err != nil {
			return status, err
		}
		if status.Status == "authorized" || status.Status == "delegated" {
			err = m.markBoundAt(pkg, generation)
		}
		return status, err
	}
	return m.Start(id)
}
