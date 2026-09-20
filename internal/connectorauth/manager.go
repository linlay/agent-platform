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
	"agent-platform/internal/hostenv"
	"agent-platform/internal/httpclient"
)

type Session struct {
	ID                  string    `json:"sessionId"`
	ConnectorID         string    `json:"connectorId"`
	ComponentID         string    `json:"componentId,omitempty"`
	Status              string    `json:"status"`
	PendingVerification bool      `json:"pendingVerification,omitempty"`
	URL                 string    `json:"authorizationUrl,omitempty"`
	AuthBrowser         string    `json:"authBrowser"`
	Message             string    `json:"message,omitempty"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

type login struct {
	Session
	generation       *string
	terminalRevision string
	cancel           context.CancelFunc
	done             chan struct{}
}

type Manager struct {
	credentialValidator func(context.Context, connector.Package, map[string]string) error
	tokenValidations    map[string]*tokenValidation
	ctx                 context.Context
	sources             connector.Sources
	reload              func(context.Context, string) error
	client              *http.Client
	identityFile        string
	mu                  sync.Mutex
	preparations        map[string]*preparationJob
	sessions            map[string]*login
	epochs              map[string]uint64
	disconnecting       map[string]bool
}

func New(ctx context.Context, sources connector.Sources, reload func(context.Context, string) error) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	go hostenv.WithNPM(os.Environ())
	return &Manager{ctx: ctx, sources: sources, reload: reload, client: httpclient.NewClient(30 * time.Second), preparations: map[string]*preparationJob{}, sessions: map[string]*login{}}
}

func (m *Manager) WithIdentityFile(path string) *Manager {
	m.identityFile = strings.TrimSpace(path)
	return m
}

func (m *Manager) epoch(id string) uint64 { m.mu.Lock(); defer m.mu.Unlock(); return m.epochs[id] }
func (m *Manager) beginDisconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disconnecting[id] {
		return fmt.Errorf("connector disconnect is in progress")
	}
	if m.disconnecting == nil {
		m.disconnecting = map[string]bool{}
	}
	m.disconnecting[id] = true
	return nil
}
func (m *Manager) endDisconnect(id string) { m.mu.Lock(); delete(m.disconnecting, id); m.mu.Unlock() }
func (m *Manager) changing(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disconnecting[id]
}
func (m *Manager) retire(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epochs == nil {
		m.epochs = map[string]uint64{}
	}
	m.epochs[id]++
}
func (m *Manager) markConfiguredAt(pkg connector.Package, epoch uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epochs[pkg.ID] != epoch {
		return fmt.Errorf("connector operation superseded")
	}
	return m.markConfigured(pkg)
}
func (m *Manager) markConfigured(pkg connector.Package) error {
	_, err := pkg.SetConfigured(true)
	return err
}

// Publish completion and cached CLI status under the same durable logout fence.
func (m *Manager) completeLogin(ctx context.Context, pkg connector.Package, epoch uint64, generation string) error {
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	state, err := readAuthState(pkg.PersistentRoot(), pkg.ID)
	if err == nil && (ctx.Err() != nil || m.epochs[pkg.ID] != epoch || m.disconnecting[pkg.ID] || state.Generation != generation) {
		err = fmt.Errorf("connector login superseded")
	}
	if err == nil {
		err = m.markConfigured(pkg)
	}
	if err == nil {
		err = changeAuthState(pkg.PersistentRoot(), pkg.ID, false)
	}
	if err == nil && pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
		err = saveVerification(pkg, nil, "authorized")
	}
	m.mu.Unlock()
	unlock()
	if err == nil && m.reload != nil {
		err = m.reload(ctx, pkg.ID)
	}
	return err
}

func (m *Manager) Start(id string) (Session, error) {
	return m.StartComponent(id, "")
}

func (m *Manager) StartComponent(id, component string) (Session, error) {
	generation := m.epoch(id)
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP {
		pkg, err = OAuthComponent(pkg, component)
		if err != nil {
			return Session{}, err
		}
	}
	if !(pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI()) && pkg.AuthMode != connector.AuthOAuth && pkg.AuthMode != connector.AuthMCP {
		return Session{}, fmt.Errorf("connector does not support interactive login")
	}
	m.mu.Lock()
	if m.disconnecting[id] {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("connector logout is in progress")
	}
	if old := m.sessions[id]; old != nil && (old.Status == "preparing" || old.Status == "pending") {
		if old.ComponentID != component {
			m.mu.Unlock()
			return Session{}, fmt.Errorf("another component authorization is pending")
		}
		result := old.Session
		m.mu.Unlock()
		return result, nil
	}
	release := func() {}
	if pkg.ManagedCLI() {
		var err error
		release, err = connector.AcquireOperation(m.sources.ExternalRoot, id)
		if err != nil {
			m.mu.Unlock()
			return Session{}, err
		}
		pkg, err = m.sources.Load(id)
		if err == nil && !pkg.ManagedCLI() {
			err = fmt.Errorf("connector does not support interactive login")
		}
		if err == nil {
			err = m.requirePrepared(pkg)
		}
		if err != nil {
			release()
			m.mu.Unlock()
			return Session{}, err
		}
	}
	state, stateErr := readAuthState(m.sources.PersistentRoot(), id)
	if stateErr != nil {
		release()
		m.mu.Unlock()
		return Session{}, stateErr
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	s := &login{Session: Session{ID: rand.Text(), ConnectorID: id, AuthBrowser: pkg.AuthorizationBrowser(), Status: "preparing", ExpiresAt: time.Now().Add(15 * time.Minute)}, cancel: cancel, done: make(chan struct{})}
	s.ComponentID = component
	s.generation = &state.Generation
	m.sessions[id] = s
	result := s.Session
	m.mu.Unlock()
	go func() {
		defer close(s.done)
		defer cancel()
		released := false
		defer func() {
			if !released {
				release()
			}
		}()
		var err error
		if pkg.AuthMode == connector.AuthDelegated && pkg.ManagedCLI() {
			err = m.loginCLI(ctx, pkg, s)
		} else {
			err = m.loginOAuth(ctx, pkg, s)
		}
		release()
		released = true
		if err == nil && ctx.Err() == nil {
			err = m.completeLogin(ctx, pkg, generation, *s.generation)
		}
		terminalState, _ := readAuthState(m.sources.PersistentRoot(), id)
		m.mu.Lock()
		defer m.mu.Unlock()
		s.terminalRevision = terminalState.Revision
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
	return m.StatusComponent(ctx, id, "")
}

func (m *Manager) StatusComponent(ctx context.Context, id, component string) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	if s := m.sessions[id]; s != nil && s.Status != "authorized" {
		state, stateErr := readAuthState(m.sources.PersistentRoot(), id)
		if s.Status == "pending" || s.Status == "preparing" || stateErr != nil || state.Revision == s.terminalRevision {
			out := s.Session
			m.mu.Unlock()
			return out, nil
		}
	}
	m.mu.Unlock()
	result := Session{ConnectorID: id, AuthBrowser: pkg.AuthorizationBrowser(), Status: "unauthorized"}
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
		if status := cachedVerification(pkg, nil); status != "" {
			result.Status = status
		}
		return result, nil
	}
	if pkg.AuthMode == connector.AuthToken {
		return m.tokenStatus(ctx, pkg)
	}
	if len(pkg.MCP) > 1 && component == "" {
		result.Status = "authorized"
		for name := range pkg.MCP {
			selected, err := OAuthComponent(pkg, name)
			if err != nil {
				return result, err
			}
			resource, _, err := oauthResource(selected)
			if err != nil {
				return result, err
			}
			if !CredentialReady(m.sources.PersistentRoot(), id, resource, oauthDestination(selected)) {
				result.Status = "unauthorized"
			}
		}
		return result, nil
	}
	pkg, err = OAuthComponent(pkg, component)
	if err != nil {
		return result, err
	}
	result.ComponentID = component
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
	return m.CancelSession(id, "")
}

// A stale browser must never cancel a replacement authorization session.
func (m *Manager) CancelSession(id, sessionID string) error {
	m.mu.Lock()
	s := m.sessions[id]
	if sessionID != "" && (s == nil || s.ID != sessionID) {
		m.mu.Unlock()
		return fmt.Errorf("authorization session has changed")
	}
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
	if pkg.AuthMode == connector.AuthOneID || pkg.AuthMode == connector.AuthDelegated && !pkg.ManagedCLI() {
		return fmt.Errorf("authentication is managed by the identity provider or connector CLI")
	}
	result, err := m.Disconnect(ctx, id)
	if err == nil && len(result.Warnings) > 0 {
		return fmt.Errorf("%s", result.Warnings[0])
	}
	return err
}
