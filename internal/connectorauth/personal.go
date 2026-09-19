package connectorauth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/connector"
)

// Personal returns an isolated authentication manager. The caller must derive
// subject from a verified principal, never from request JSON. Installation and
// preparation remain deployment-owned; credentials never fall back to it.
func (m *Manager) Personal(subject, binding string) (*Manager, error) {
	if strings.TrimSpace(subject) == "" || len(subject) > 1024 || !connector.ValidID(binding) || len(binding) > 128 {
		return nil, fmt.Errorf("invalid personal connector identity")
	}
	digest := sha256.Sum256([]byte(subject))
	owner := hex.EncodeToString(digest[:])
	root := m.sources.PersistentRoot()
	for _, name := range []string{"users", owner, "bindings", binding} {
		root = filepath.Join(root, name)
		if info, err := os.Lstat(root); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("invalid personal connector state directory")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.personal == nil {
		m.personal = make(map[string]*Manager)
	}
	if scoped := m.personal[root]; scoped != nil {
		return scoped, nil
	}
	if len(m.personal) >= 1024 {
		return nil, fmt.Errorf("personal connector capacity exceeded")
	}
	sources := m.sources
	sources.StateRoot = root
	scoped := &Manager{ctx: m.ctx, sources: sources, client: m.client,
		preparationOwner: m, preparations: map[string]*preparationJob{},
		sessions: map[string]*login{}, loggingOut: map[string]bool{}}
	m.personal[root] = scoped
	return scoped, nil
}

// Package returns only the package description and isolated credential locator.
func (m *Manager) Package(id string) (connector.Package, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return pkg, err
	}
	if m.preparationOwner != nil {
		if pkg.AuthMode == connector.AuthOneID {
			return pkg, fmt.Errorf("personal connector cannot use deployment identity-file")
		}
		if pkg.CLI != nil {
			platform, _ := pkg.CLI["platform"].(map[string]any)
			if platform["personalConfig"] != true {
				return pkg, fmt.Errorf("connector package must declare personalConfig support")
			}
			env, err := pkg.CLIConfigEnvironment()
			if err != nil || len(env) != 1 {
				return pkg, fmt.Errorf("personal CLI requires an isolated configEnv")
			}
		}
	}
	return pkg, nil
}

// Revision changes after login/refresh/logout and is suitable for fencing a
// result. It is not an authentication token and is never a credential.
func (m *Manager) Revision(id string) (string, error) {
	s, err := readAuthState(m.sources.PersistentRoot(), id)
	return s.Generation + ":" + s.Revision, err
}

// SessionStatus addresses one exact attempt. Unlike StatusComponent, it does not
// invoke CLI status or silently return a replacement session.
func (m *Manager) SessionStatus(id, sessionID string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if sessionID == "" || s == nil || s.ID != sessionID {
		return Session{}, fmt.Errorf("authorization session unavailable")
	}
	result := s.Session
	if !time.Now().Before(s.ExpiresAt) && (result.Status == "pending" || result.Status == "preparing") {
		result.Status = "expired"
		result.URL = ""
		s.cancel()
	}
	return result, nil
}
