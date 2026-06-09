package terminal

import (
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
)

var (
	ErrNotFound    = errors.New("terminal not found")
	ErrUnsupported = errors.New("terminal unsupported")
)

type OpenRequest struct {
	AgentKey string
	ChatID   string
	CWD      string
	Shell    string
	Cols     int
	Rows     int
	Env      []string
}

type Event struct {
	Type       string
	TerminalID string
	AgentKey   string
	ChatID     string
	CWD        string
	Shell      string
	Data       string
	ExitCode   *int
	Reason     string
}

type Manager struct {
	mu       sync.RWMutex
	nextID   atomic.Int64
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

func (m *Manager) Open(req OpenRequest) (*Session, error) {
	if m == nil {
		return nil, fmt.Errorf("terminal manager is nil")
	}
	req.AgentKey = strings.TrimSpace(req.AgentKey)
	req.ChatID = strings.TrimSpace(req.ChatID)
	req.CWD = strings.TrimSpace(req.CWD)
	req.Shell = strings.TrimSpace(req.Shell)
	req.Cols, req.Rows = normalizeSize(req.Cols, req.Rows)
	if req.AgentKey == "" {
		return nil, fmt.Errorf("agentKey is required")
	}
	if req.CWD == "" {
		return nil, fmt.Errorf("cwd is required")
	}
	if req.Shell == "" {
		return nil, fmt.Errorf("shell is required")
	}

	proc, err := startPTY(startPTYRequest{
		Shell: req.Shell,
		CWD:   req.CWD,
		Cols:  req.Cols,
		Rows:  req.Rows,
		Env:   req.Env,
	})
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("term_%d", m.nextID.Add(1))
	session := &Session{
		id:        id,
		agentKey:  req.AgentKey,
		chatID:    req.ChatID,
		cwd:       req.CWD,
		shell:     req.Shell,
		proc:      proc,
		events:    make(chan Event, 128),
		startedAt: time.Now(),
	}
	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()
	return session, nil
}

func (m *Manager) Input(terminalID string, data string) error {
	session, ok := m.lookup(terminalID)
	if !ok {
		return ErrNotFound
	}
	return session.Write(data)
}

func (m *Manager) Resize(terminalID string, cols int, rows int) error {
	session, ok := m.lookup(terminalID)
	if !ok {
		return ErrNotFound
	}
	cols, rows = normalizeSize(cols, rows)
	return session.Resize(cols, rows)
}

func (m *Manager) Close(terminalID string) error {
	session, ok := m.lookup(terminalID)
	if !ok {
		return ErrNotFound
	}
	session.Close("closed")
	return nil
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
	delete(m.sessions, strings.TrimSpace(terminalID))
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
