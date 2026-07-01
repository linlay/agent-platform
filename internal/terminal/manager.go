package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EventOpened = "terminal.opened"
	EventOutput = "terminal.output"
	EventExit   = "terminal.exit"

	DefaultTerminalKey = "main"
	ScopeAgent         = "agent"
	StatusIdle         = "idle"
	StatusBusy         = "busy"

	maxTerminalKeyBytes         = 64
	maxSessionsPerOwnerAndAgent = 8
	maxSessionsTotal            = 128
)

var (
	ErrForbidden       = errors.New("terminal access denied")
	ErrInvalidKey      = errors.New("invalid terminal key")
	ErrNotFound        = errors.New("terminal not found")
	ErrSessionLimit    = errors.New("terminal session limit exceeded")
	ErrSessionConflict = errors.New("terminal session conflict")
	ErrUnsupported     = errors.New("terminal unsupported")
)

type OpenRequest struct {
	OwnerKey    string
	AgentKey    string
	TerminalKey string
	ChatID      string
	CWD         string
	Shell       string
	Cols        int
	Rows        int
	Env         []string
}

type OpenResult struct {
	Session *Session
	Reused  bool
}

type Event struct {
	Type        string
	TerminalID  string
	AgentKey    string
	TerminalKey string
	Scope       string
	ChatID      string
	CWD         string
	Shell       string
	Data        string
	ExitCode    *int
	Reason      string
	Reused      bool
	Replay      bool
}

type Manager struct {
	mu            sync.RWMutex
	nextID        atomic.Int64
	sessions      map[string]*Session
	agentSessions map[string]string
}

func NewManager() *Manager {
	return &Manager{
		sessions:      map[string]*Session{},
		agentSessions: map[string]string{},
	}
}

func (m *Manager) Open(req OpenRequest) (OpenResult, error) {
	if m == nil {
		return OpenResult{}, fmt.Errorf("terminal manager is nil")
	}
	req.OwnerKey = strings.TrimSpace(req.OwnerKey)
	req.AgentKey = strings.TrimSpace(req.AgentKey)
	terminalKey, keyErr := normalizeTerminalKey(req.TerminalKey)
	if keyErr != nil {
		return OpenResult{}, keyErr
	}
	req.TerminalKey = terminalKey
	req.ChatID = strings.TrimSpace(req.ChatID)
	req.CWD = strings.TrimSpace(req.CWD)
	req.Shell = strings.TrimSpace(req.Shell)
	req.Cols, req.Rows = normalizeSize(req.Cols, req.Rows)
	if req.OwnerKey == "" {
		return OpenResult{}, fmt.Errorf("ownerKey is required")
	}
	if req.AgentKey == "" {
		return OpenResult{}, fmt.Errorf("agentKey is required")
	}
	if req.CWD == "" {
		return OpenResult{}, fmt.Errorf("cwd is required")
	}
	if req.Shell == "" {
		return OpenResult{}, fmt.Errorf("shell is required")
	}

	registryKey := agentSessionKey(req.OwnerKey, req.AgentKey, req.TerminalKey)
	if session, ok, err := m.lookupAgentSession(registryKey, req.CWD, req.Shell); ok || err != nil {
		return OpenResult{Session: session, Reused: ok}, err
	}
	if err := m.checkSessionLimits(req.OwnerKey, req.AgentKey); err != nil {
		return OpenResult{}, err
	}

	proc, err := startPTY(startPTYRequest{
		Shell: req.Shell,
		CWD:   req.CWD,
		Cols:  req.Cols,
		Rows:  req.Rows,
		Env:   req.Env,
	})
	if err != nil {
		return OpenResult{}, err
	}

	id := newTerminalID(m.nextID.Add(1))
	session := &Session{
		id:          id,
		ownerKey:    req.OwnerKey,
		agentKey:    req.AgentKey,
		terminalKey: req.TerminalKey,
		scope:       ScopeAgent,
		cwd:         req.CWD,
		shell:       req.Shell,
		proc:        proc,
		subscribers: map[int64]chan Event{},
		done:        make(chan struct{}),
		startedAt:   time.Now(),
	}
	m.mu.Lock()
	if sessionID := m.agentSessions[registryKey]; sessionID != "" {
		existing := m.sessions[sessionID]
		if existing != nil {
			if existing.Finished() {
				delete(m.sessions, sessionID)
			} else {
				m.mu.Unlock()
				_ = proc.Close()
				_, _ = proc.Wait()
				if existing.CWD() != req.CWD || existing.Shell() != req.Shell {
					return OpenResult{}, fmt.Errorf("%w: terminalKey already exists with different cwd or shell", ErrSessionConflict)
				}
				return OpenResult{Session: existing, Reused: true}, nil
			}
		}
	}
	if err := m.checkSessionLimitsLocked(req.OwnerKey, req.AgentKey); err != nil {
		m.mu.Unlock()
		_ = proc.Close()
		_, _ = proc.Wait()
		return OpenResult{}, err
	}
	for {
		if _, exists := m.sessions[session.id]; !exists {
			break
		}
		session.id = newTerminalID(m.nextID.Add(1))
	}
	m.sessions[session.id] = session
	m.agentSessions[registryKey] = session.id
	m.mu.Unlock()
	return OpenResult{Session: session}, nil
}

func (m *Manager) Input(ownerKey string, terminalID string, data string) error {
	session, err := m.lookupOwned(ownerKey, terminalID)
	if err != nil {
		return err
	}
	return session.Write(data)
}

func (m *Manager) Resize(ownerKey string, terminalID string, cols int, rows int) error {
	session, err := m.lookupOwned(ownerKey, terminalID)
	if err != nil {
		return err
	}
	cols, rows = normalizeSize(cols, rows)
	return session.Resize(cols, rows)
}

func (m *Manager) Close(ownerKey string, terminalID string) error {
	session, err := m.lookupOwned(ownerKey, terminalID)
	if err != nil {
		return err
	}
	session.Close("closed")
	return nil
}

func (m *Manager) Discard(session *Session) {
	if m == nil || session == nil {
		return
	}
	session.Close("closed")
	m.remove(session.ID())
}

func (m *Manager) Start(session *Session) {
	if m == nil || session == nil {
		return
	}
	session.Start(m.remove)
}

func (m *Manager) remove(terminalID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	terminalID = strings.TrimSpace(terminalID)
	session := m.sessions[terminalID]
	if session != nil {
		registryKey := agentSessionKey(session.OwnerKey(), session.AgentKey(), session.TerminalKey())
		if m.agentSessions[registryKey] == terminalID {
			delete(m.agentSessions, registryKey)
		}
	}
	delete(m.sessions, terminalID)
	m.mu.Unlock()
}

func (m *Manager) lookup(terminalID string) (*Session, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	session, ok := m.sessions[strings.TrimSpace(terminalID)]
	m.mu.RUnlock()
	return session, ok
}

func (m *Manager) lookupOwned(ownerKey string, terminalID string) (*Session, error) {
	ownerKey = strings.TrimSpace(ownerKey)
	if ownerKey == "" {
		return nil, ErrForbidden
	}
	session, ok := m.lookup(terminalID)
	if !ok || session == nil || session.Finished() {
		return nil, ErrNotFound
	}
	if session.OwnerKey() != ownerKey {
		return nil, ErrNotFound
	}
	return session, nil
}

func (m *Manager) lookupAgentSession(registryKey string, cwd string, shell string) (*Session, bool, error) {
	m.mu.RLock()
	sessionID := m.agentSessions[registryKey]
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil || session.Finished() {
		return nil, false, nil
	}
	if session.CWD() != cwd || session.Shell() != shell {
		return nil, false, fmt.Errorf("%w: terminalKey already exists with different cwd or shell", ErrSessionConflict)
	}
	return session, true, nil
}

func (m *Manager) checkSessionLimits(ownerKey string, agentKey string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.checkSessionLimitsLocked(ownerKey, agentKey)
}

func (m *Manager) checkSessionLimitsLocked(ownerKey string, agentKey string) error {
	total := 0
	ownerAgentCount := 0
	for _, session := range m.sessions {
		if session == nil || session.Finished() {
			continue
		}
		total++
		if session.OwnerKey() == ownerKey && session.AgentKey() == agentKey {
			ownerAgentCount++
		}
	}
	if total >= maxSessionsTotal {
		return fmt.Errorf("%w: too many terminal sessions", ErrSessionLimit)
	}
	if ownerAgentCount >= maxSessionsPerOwnerAndAgent {
		return fmt.Errorf("%w: too many terminals for this agent", ErrSessionLimit)
	}
	return nil
}

func normalizeTerminalKey(terminalKey string) (string, error) {
	terminalKey = strings.TrimSpace(terminalKey)
	if terminalKey == "" {
		return DefaultTerminalKey, nil
	}
	if len(terminalKey) > maxTerminalKeyBytes {
		return "", fmt.Errorf("%w: terminalKey is too long", ErrInvalidKey)
	}
	for _, ch := range terminalKey {
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return "", fmt.Errorf("%w: terminalKey contains unsupported characters", ErrInvalidKey)
	}
	return terminalKey, nil
}

func agentSessionKey(ownerKey string, agentKey string, terminalKey string) string {
	return strings.TrimSpace(ownerKey) + "\x00" + strings.TrimSpace(agentKey) + "\x00" + strings.TrimSpace(terminalKey)
}

func newTerminalID(seq int64) string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return "term_" + hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("term_%d_%d", time.Now().UnixNano(), seq)
}

func normalizeSize(cols int, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if cols < 2 {
		cols = 2
	}
	if rows < 2 {
		rows = 2
	}
	if cols > 500 {
		cols = 500
	}
	if rows > 200 {
		rows = 200
	}
	return cols, rows
}
