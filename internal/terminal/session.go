package terminal

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const replayBufferLimitBytes = 256 * 1024

type Session struct {
	id          string
	ownerKey    string
	agentKey    string
	terminalKey string
	scope       string
	cwd         string
	shell       string
	proc        ptyProcess
	startOnce   sync.Once
	closeOnce   sync.Once
	mu          sync.RWMutex
	subscribers map[int64]chan Event
	replay      []byte
	finished    atomic.Bool
	done        chan struct{}
	reason      string
	startedAt   time.Time
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

func (s *Session) OwnerKey() string {
	if s == nil {
		return ""
	}
	return s.ownerKey
}

func (s *Session) TerminalKey() string {
	if s == nil {
		return ""
	}
	return s.terminalKey
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

func (s *Session) Status() string {
	if s == nil || s.finished.Load() || s.proc == nil {
		return StatusIdle
	}
	if s.proc.Busy() {
		return StatusBusy
	}
	return StatusIdle
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

func (s *Session) publish(event Event) {
	if s == nil {
		return
	}
	event = s.event(event)
	s.mu.Lock()
	if s.finished.Load() {
		s.mu.Unlock()
		return
	}
	if event.Type == EventOutput && !event.Replay {
		s.appendReplayLocked(event.Data)
	}
	var stale []int64
	for id, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
			stale = append(stale, id)
			close(ch)
		}
	}
	for _, id := range stale {
		delete(s.subscribers, id)
	}
	s.mu.Unlock()
}

func (s *Session) event(event Event) Event {
	if event.TerminalID == "" {
		event.TerminalID = s.id
	}
	if event.AgentKey == "" {
		event.AgentKey = s.agentKey
	}
	if event.TerminalKey == "" {
		event.TerminalKey = s.terminalKey
	}
	if event.Scope == "" {
		event.Scope = s.scope
	}
	if event.CWD == "" {
		event.CWD = s.cwd
	}
	if event.Shell == "" {
		event.Shell = s.shell
	}
	return event
}

func (s *Session) appendReplayLocked(data string) {
	if data == "" {
		return
	}
	s.replay = append(s.replay, []byte(data)...)
	if len(s.replay) <= replayBufferLimitBytes {
		return
	}
	start := len(s.replay) - replayBufferLimitBytes
	for start < len(s.replay) && !utf8.RuneStart(s.replay[start]) {
		start++
	}
	s.replay = append([]byte(nil), s.replay[start:]...)
}

func (s *Session) finishSubscribers() {
	if !s.finished.CompareAndSwap(false, true) {
		return
	}
	if s.done != nil {
		close(s.done)
	}
	s.mu.Lock()
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Session) readLoop(onDone func(string)) {
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
	defer s.finishSubscribers()

	buf := make([]byte, 8192)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.publish(Event{Type: EventOutput, Data: string(buf[:n])})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
	}

	exitCode, _ := s.proc.Wait()
	s.publish(Event{Type: EventExit, ExitCode: exitCode, Reason: s.closeReason()})
}
