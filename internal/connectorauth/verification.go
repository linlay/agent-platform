package connectorauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-platform/internal/connector"
)

type verification struct {
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	CheckedAt   int64  `json:"checkedAt"`
}

func verificationFingerprint(pkg connector.Package, values map[string]string) string {
	state, _ := readAuthState(pkg.PersistentRoot(), pkg.ID)
	data, _ := json.Marshal([]any{pkg.Manifest, pkg.CLI, pkg.MCP, values, state.Generation})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func saveVerification(pkg connector.Package, values map[string]string, status string) error {
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return err
	}
	return savePrivateJSON(filepath.Join(dir, "verification.json"), verification{Fingerprint: verificationFingerprint(pkg, values), Status: status, CheckedAt: time.Now().UnixMilli()})
}
func cachedVerification(pkg connector.Package, values map[string]string) string {
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return ""
	}
	var v verification
	if readPrivateJSON(filepath.Join(dir, "verification.json"), &v) != nil || v.Fingerprint != verificationFingerprint(pkg, values) {
		return ""
	}
	return v.Status
}
func pendingTokenValues(pkg connector.Package) (map[string]string, bool, error) {
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return nil, false, err
	}
	var values map[string]string
	err = readPrivateJSON(filepath.Join(dir, "pending-credentials.json"), &values)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("pending credentials unavailable")
	}
	if err = connector.ValidateTokenValues(pkg.Manifest, values); err != nil {
		return nil, false, fmt.Errorf("pending credentials invalid")
	}
	return values, true, nil
}

// Check is explicit: only this action and credential submission run verification probes.
func (m *Manager) Check(ctx context.Context, id, component string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.AuthMode == connector.AuthToken {
		return m.checkToken(ctx, pkg)
	}
	if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
		generation := m.epoch(id)
		checkCtx, _, finish, err := m.beginTokenValidation(ctx, id)
		if err != nil {
			return Session{}, err
		}
		defer finish()
		release, err := connector.AcquireOperation(m.sources.ExternalRoot, id)
		if err != nil {
			return Session{}, err
		}
		defer release()
		ok, err := m.cliStatus(checkCtx, pkg)
		status := "unauthorized"
		if err != nil {
			status = "failed"
		} else if ok {
			status = "authorized"
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if checkCtx.Err() != nil || m.epochs[id] != generation || m.disconnecting[id] {
			return Session{}, fmt.Errorf("connector check superseded")
		}
		if err = saveVerification(pkg, nil, status); err != nil {
			return Session{}, err
		}
		// A completed check supersedes an earlier failed or canceled login,
		// while an active login keeps ownership of its progress status.
		if previous := m.sessions[id]; previous != nil && previous.Status != "pending" && previous.Status != "preparing" {
			delete(m.sessions, id)
		}
		return Session{ConnectorID: id, Status: status, AuthBrowser: pkg.AuthorizationBrowser()}, nil
	}
	// OAuth uses the existing locally stored grant/expiry contract; OneID reads Desktop identity.
	return m.StatusComponent(ctx, id, component)
}
