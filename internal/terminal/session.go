package terminal

import (
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

type Session struct {
	id        string
	agentKey  string
	chatID    string
	cwd       string
	shell     string
	proc      ptyProcess
	events    chan Event
	startOnce sync.Once
	closeOnce sync.Once
	mu        sync.RWMutex
	reason    string
	startedAt time.Time
}

func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *Session) AgentKey() string {
	if s == nil {
		return ""
	}
	return s.agentKey
}

func (s *Session) CWD() string {
	if s == nil {
		return ""
	}
	return s.cwd
}

func (s *Session) Shell() string {
	if s == nil {
		return ""
	}
	return s.shell
}

func (s *Session) Events() <-chan Event {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *Session) Start(onDone func(string)) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.readLoop(onDone)
	})
}

func (s *Session) Write(data string) error {
	if s == nil || s.proc == nil {
		return ErrNotFound
	}
	if data == "" {
		return nil
	}
	_, err := s.proc.Write([]byte(data))
	return err
}

func (s *Session) Resize(cols int, rows int) error {
	if s == nil || s.proc == nil {
		return ErrNotFound
	}
	return s.proc.Resize(cols, rows)
}

func (s *Session) Close(reason string) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "closed"
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.reason = reason
		s.mu.Unlock()
		if s.proc != nil {
			_ = s.proc.Close()
		}
	})
}

func (s *Session) closeReason() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	reason := s.reason
	s.mu.RUnlock()
	if strings.TrimSpace(reason) == "" {
		return "exit"
	}
	return reason
}

func (s *Session) readLoop(onDone func(string)) {
	defer close(s.events)
	defer func() {
		if onDone != nil {
			onDone(s.id)
		}
	}()
	defer func() {
		if s.proc != nil {
			_ = s.proc.Close()
		}
	}()

	buf := make([]byte, 8192)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.events <- Event{
				Type:       EventOutput,
				TerminalID: s.id,
				AgentKey:   s.agentKey,
				ChatID:     s.chatID,
				CWD:        s.cwd,
				Shell:      s.shell,
				Data:       string(buf[:n]),
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
	}

	exitCode, _ := s.proc.Wait()
	s.events <- Event{
		Type:       EventExit,
		TerminalID: s.id,
		AgentKey:   s.agentKey,
		ChatID:     s.chatID,
		CWD:        s.cwd,
		Shell:      s.shell,
		ExitCode:   exitCode,
		Reason:     s.closeReason(),
	}
}
