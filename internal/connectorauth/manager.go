package connectorauth

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
)

type Session struct {
	ID          string    `json:"sessionId"`
	ConnectorID string    `json:"connectorId"`
	Status      string    `json:"status"`
	URL         string    `json:"authorizationUrl,omitempty"`
	Message     string    `json:"message,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type login struct {
	Session
	cancel context.CancelFunc
	done   chan struct{}
}

type Manager struct {
	ctx          context.Context
	sources      connector.Sources
	reload       func(context.Context, string) error
	client       *http.Client
	identityFile string
	mu           sync.Mutex
	sessions     map[string]*login
	loggingOut   map[string]bool
}

func New(ctx context.Context, sources connector.Sources, reload func(context.Context, string) error) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{ctx: ctx, sources: sources, reload: reload, client: &http.Client{Timeout: 30 * time.Second}, sessions: map[string]*login{}, loggingOut: map[string]bool{}}
}

func (m *Manager) WithIdentityFile(path string) *Manager {
	m.identityFile = strings.TrimSpace(path)
	return m
}

func (m *Manager) Start(id string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.Builtin {
		return Session{}, connector.ErrBuiltinReadOnly
	}
	if !(pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI()) && pkg.AuthMode != connector.AuthOAuth && pkg.AuthMode != connector.AuthMCP {
		return Session{}, fmt.Errorf("connector does not support interactive login")
	}
	m.mu.Lock()
	if m.loggingOut[id] {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("connector logout is in progress")
	}
	if old := m.sessions[id]; old != nil && (old.Status == "preparing" || old.Status == "pending") {
		result := old.Session
		m.mu.Unlock()
		return result, nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	s := &login{Session: Session{ID: rand.Text(), ConnectorID: id, Status: "preparing", ExpiresAt: time.Now().Add(15 * time.Minute)}, cancel: cancel, done: make(chan struct{})}
	m.sessions[id] = s
	result := s.Session
	m.mu.Unlock()
	go func() {
		defer close(s.done)
		defer cancel()
		var err error
		if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
			err = m.loginCLI(ctx, pkg, s)
		} else {
			err = m.loginOAuth(ctx, pkg, s)
		}
		if err == nil && ctx.Err() == nil && m.reload != nil {
			err = m.reload(ctx, id)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		s.URL = ""
		if ctx.Err() != nil {
			s.Status = "canceled"
			s.Message = "Login canceled or expired"
		} else if err != nil {
			s.Status = "failed"
			s.Message = err.Error()
		} else {
			s.Status = "authorized"
			s.Message = "Login completed"
		}
	}()
	return result, nil
}

func (m *Manager) setURL(s *login, u string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.Status = "pending"
	s.URL = u
	s.Message = "Open the authorization link and complete login"
}

func (m *Manager) Status(ctx context.Context, id string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	if s := m.sessions[id]; s != nil && s.Status != "authorized" {
		out := s.Session
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()
	result := Session{ConnectorID: id, Status: "unauthorized"}
	if pkg.AuthMode == connector.AuthOneID {
		identity, err := agentconfig.ReadIdentityEnvironment(m.identityFile)
		if err == nil && identity[agentconfig.EnvAccessToken] != "" {
			result.Status = "authorized"
		} else {
			result.Message = "Desktop SSO is unavailable; sign in through Desktop"
		}
		return result, nil
	}
	if pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI() {
		result.Status = "delegated"
		result.Message = "Authentication is handled by the connector skill or CLI"
		return result, nil
	}
	if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
		ok, err := m.cliStatus(ctx, pkg)
		if err != nil {
			result.Status = "setup_required"
			result.Message = err.Error()
		} else if ok {
			result.Status = "authorized"
		}
		return result, nil
	}
	if pkg.AuthMode == connector.AuthToken {
		_, ready, err := TokenValues(pkg)
		if err != nil {
			return result, err
		}
		if ready {
			result.Status = "authorized"
		}
		return result, nil
	}
	resource, _, err := oauthResource(pkg)
	if err != nil {
		return result, err
	}
	if CredentialReady(m.sources.PersistentRoot(), id, resource, oauthDestination(pkg)) {
		result.Status = "authorized"
	}
	return result, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil {
		s.cancel()
	}
	m.mu.Unlock()
	if s != nil {
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("login cancellation is still in progress")
		}
	}
	return nil
}

func (m *Manager) Logout(ctx context.Context, id string) error {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return err
	}
	if pkg.Builtin {
		return connector.ErrBuiltinReadOnly
	}
	if pkg.AuthMode == connector.AuthOneID || pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI() {
		return fmt.Errorf("authentication is managed by the identity provider or connector CLI")
	}
	m.mu.Lock()
	if m.loggingOut[id] {
		m.mu.Unlock()
		return fmt.Errorf("connector logout is in progress")
	}
	m.loggingOut[id] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.loggingOut, id); m.mu.Unlock() }()
	if err := m.Cancel(id); err != nil {
		return err
	}
	if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
		err = m.logoutCLI(ctx, pkg)
	} else {
		unlock, lockErr := lockCredentials(ctx, m.sources.PersistentRoot(), id)
		if lockErr != nil {
			return lockErr
		}
		var p string
		if pkg.AuthMode == connector.AuthToken {
			p, err = connector.CredentialsPath(m.sources.PersistentRoot(), id)
		} else {
			p, err = credentialPath(m.sources.PersistentRoot(), id)
		}
		if err == nil {
			err = os.Remove(p)
			if os.IsNotExist(err) {
				err = nil
			}
		}
		unlock()
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	if m.reload != nil {
		return m.reload(ctx, id)
	}
	return nil
}
