package connectorauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/connector"
)

type Capabilities struct {
	CanConnect    bool               `json:"canConnect"`
	CanDisconnect bool               `json:"canDisconnect"`
	CanCheck      bool               `json:"canCheck"`
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

// Connection is a local snapshot. Reading a list must not launch a CLI or contact an upstream.
func (m *Manager) Connection(ctx context.Context, id string) (Connection, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Connection{}, err
	}
	state, err := pkg.ReadConnection()
	if err != nil {
		return Connection{}, err
	}
	c := Connection{ConnectionState: state, Readiness: "configuration_required", Capabilities: Capabilities{CanConnect: pkg.AuthMode != connector.AuthToken, CanDisconnect: true, CanCheck: true, AuthMode: pkg.AuthMode, AuthBrowser: pkg.AuthorizationBrowser(), HasCLI: pkg.CLI != nil, HasMCP: len(pkg.MCP) > 0}}
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
	auth, err := m.Status(ctx, id)
	if err != nil {
		c.Readiness = "unavailable"
		c.Authentication = Session{ConnectorID: id, Status: "failed", Message: "Authorization status unavailable"}
		return c, nil
	}
	c.Authentication = auth
	if !state.Configured {
		return c, nil
	}
	switch auth.Status {
	case "authorized", "delegated", "configured":
		c.Readiness = "ready"
	case "preparing", "pending":
		c.Readiness = "preparing"
	case "pending_verification":
		c.Readiness = "pending_verification"
	case "unauthorized":
		c.Readiness = "authorization_required"
	default:
		c.Readiness = "unavailable"
	}
	return c, nil
}

type DisconnectResult struct {
	ConnectorID string   `json:"connectorId"`
	Configured  bool     `json:"configured"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Disconnect clears local authorization. Already-dispatched business calls may finish.
// It never resets a CLI's private HOME/config/data, removes a package, or logs out Desktop SSO.
func (m *Manager) Disconnect(ctx context.Context, id string) (DisconnectResult, error) {
	result := DisconnectResult{ConnectorID: id}
	pkg, err := m.sources.Load(id)
	if err != nil {
		return result, err
	}
	if err = m.beginDisconnect(id); err != nil {
		return result, err
	}
	defer m.endDisconnect(id)
	m.retire(id)
	if _, err = pkg.SetConfigured(false); err != nil {
		return result, err
	}
	if err = m.cancelTokenValidation(ctx, id); err != nil {
		return result, err
	}
	if err = m.Cancel(id); err != nil {
		return result, err
	}
	if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
		release, lockErr := connector.AcquireOperation(m.sources.ExternalRoot, id)
		if lockErr != nil {
			return result, lockErr
		}
		err = m.logoutCLI(ctx, pkg)
		release()
		if err != nil {
			result.Warnings = append(result.Warnings, "CLI sign-out did not complete; private settings were preserved")
		}
	}
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), id)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = changeAuthState(pkg.PersistentRoot(), id, true); err != nil {
		return result, err
	}
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return result, err
	}
	for _, name := range []string{"credentials.json", "pending-credentials.json", "verification.json", "oauth.json", "oauth-resources"} {
		if err = os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return result, fmt.Errorf("connector authorization cleanup failed")
		}
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	_, err = pkg.SetConfigured(false)
	return result, err
}

func (m *Manager) Connect(id string) (Session, error) { return m.ConnectComponent(id, "") }
func (m *Manager) ConnectComponent(id, component string) (Session, error) {
	generation := m.epoch(id)
	if m.changing(id) {
		return Session{}, fmt.Errorf("connector disconnect is in progress")
	}
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.Type == "native" {
		err := m.markConfiguredAt(pkg, generation)
		return Session{ConnectorID: id, Status: "configured"}, err
	}
	if pkg.AuthMode == connector.AuthToken {
		return Session{}, fmt.Errorf("configure token credentials using the token endpoint")
	}
	if pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI() || pkg.AuthMode == connector.AuthOneID {
		status, err := m.Status(m.ctx, id)
		if err != nil {
			return status, err
		}
		if status.Status == "authorized" || status.Status == "delegated" {
			err = m.markConfiguredAt(pkg, generation)
		}
		return status, err
	}
	return m.StartComponent(id, component)
}
