//go:build !windows

package terminal

import (
	"strings"
	"testing"
	"time"
)

func TestManagerRunsInteractivePTY(t *testing.T) {
	manager := NewManager()
	result, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         t.TempDir(),
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	session := result.Session
	manager.Start(session)
	defer session.Close("closed")
	subscription := session.Subscribe(false)
	defer subscription.Close()

	if err := manager.Input("owner-a", session.ID(), "printf terminal-ready\\n\nexit\n"); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(5 * time.Second)
	var output strings.Builder
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				t.Fatalf("events closed before exit; output=%q", output.String())
			}
			if event.Type == EventOutput {
				output.WriteString(event.Data)
			}
			if event.Type == EventExit {
				if !strings.Contains(output.String(), "terminal-ready") {
					t.Fatalf("expected terminal output, got %q", output.String())
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal output; output=%q", output.String())
		}
	}
}

func TestManagerOpen_reusesAgentTerminalAndReplaysOutput_whenSameKeyReattaches(t *testing.T) {
	manager := NewManager()
	workspace := t.TempDir()
	first, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open first terminal: %v", err)
	}
	if first.Reused {
		t.Fatalf("first open should not be reused")
	}
	manager.Start(first.Session)
	defer first.Session.Close("closed")
	firstSub := first.Session.Subscribe(false)

	if err := manager.Input("owner-a", first.Session.ID(), "printf agent-terminal-shared\\n\n"); err != nil {
		t.Fatalf("input first terminal: %v", err)
	}
	waitForTerminalOutput(t, firstSub.Events(), "agent-terminal-shared")
	firstSub.Close()

	second, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        120,
		Rows:        30,
	})
	if err != nil {
		t.Fatalf("open second terminal: %v", err)
	}
	if !second.Reused {
		t.Fatalf("second open should reuse existing terminal")
	}
	if second.Session.ID() != first.Session.ID() {
		t.Fatalf("terminal id = %q, want %q", second.Session.ID(), first.Session.ID())
	}
	secondSub := second.Session.Subscribe(true)
	defer secondSub.Close()
	replay := waitForTerminalOutput(t, secondSub.Events(), "agent-terminal-shared")
	if !replay.Replay {
		t.Fatalf("expected replay output, got %#v", replay)
	}

	if err := manager.Input("owner-a", second.Session.ID(), "printf still-live\\n\nexit\n"); err != nil {
		t.Fatalf("input reused terminal: %v", err)
	}
	waitForTerminalOutput(t, secondSub.Events(), "still-live")
	waitForTerminalExit(t, secondSub.Events())
}

func TestManagerOpen_isolatesAgentTerminalByOwner(t *testing.T) {
	manager := NewManager()
	workspace := t.TempDir()
	first, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open first terminal: %v", err)
	}
	manager.Start(first.Session)
	defer first.Session.Close("closed")

	if err := manager.Input("owner-b", first.Session.ID(), "printf should-not-run\\n\n"); err != ErrNotFound {
		t.Fatalf("cross-owner input error = %v, want ErrNotFound", err)
	}

	second, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-b",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open second owner terminal: %v", err)
	}
	manager.Start(second.Session)
	defer second.Session.Close("closed")
	if second.Reused {
		t.Fatalf("cross-owner open should not reuse terminal")
	}
	if second.Session.ID() == first.Session.ID() {
		t.Fatalf("cross-owner terminal id reused: %q", second.Session.ID())
	}
}

func TestManagerOpen_isolatesAgentTerminalByAgentKey(t *testing.T) {
	manager := NewManager()
	workspace := t.TempDir()
	first, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder-alpha",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open first agent terminal: %v", err)
	}
	manager.Start(first.Session)
	defer first.Session.Close("closed")

	second, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder-beta",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open second agent terminal: %v", err)
	}
	manager.Start(second.Session)
	defer second.Session.Close("closed")
	if second.Reused {
		t.Fatalf("same-owner cross-agent open should not reuse terminal")
	}
	if second.Session.ID() == first.Session.ID() {
		t.Fatalf("cross-agent terminal id reused: %q", second.Session.ID())
	}
	infos := manager.List("owner-a")
	if len(infos) != 2 {
		t.Fatalf("expected two active owner sessions, got %#v", infos)
	}
	if infos[0].AgentKey != "coder-alpha" || infos[1].AgentKey != "coder-beta" {
		t.Fatalf("sessions should be sorted and scoped by agent key, got %#v", infos)
	}
}

func TestManagerListReportsBusyOnlyForForegroundCommand(t *testing.T) {
	manager := NewManager()
	result, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         t.TempDir(),
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	manager.Start(result.Session)
	defer result.Session.Close("closed")

	waitForTerminalListStatus(t, manager, "owner-a", StatusIdle)
	if err := manager.Input("owner-a", result.Session.ID(), "sleep 2\n"); err != nil {
		t.Fatalf("input sleep: %v", err)
	}
	waitForTerminalListStatus(t, manager, "owner-a", StatusBusy)
	waitForTerminalListStatus(t, manager, "owner-a", StatusIdle)
}

func TestManagerOpen_rejectsInvalidTerminalKey(t *testing.T) {
	manager := NewManager()
	_, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: strings.Repeat("x", maxTerminalKeyBytes+1),
		CWD:         t.TempDir(),
		Shell:       "/bin/sh",
	})
	if err == nil || !strings.Contains(err.Error(), ErrInvalidKey.Error()) {
		t.Fatalf("expected invalid terminal key error, got %v", err)
	}
}

func TestManagerDiscardRemovesUnstartedSessionFromRegistry(t *testing.T) {
	manager := NewManager()
	workspace := t.TempDir()
	first, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
	})
	if err != nil {
		t.Fatalf("open first terminal: %v", err)
	}
	manager.Discard(first.Session)
	if _, ok := manager.sessions[first.Session.ID()]; ok {
		t.Fatalf("discarded session remained in sessions map")
	}
	second, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
	})
	if err != nil {
		t.Fatalf("open replacement terminal: %v", err)
	}
	manager.Start(second.Session)
	defer second.Session.Close("closed")
	if second.Reused {
		t.Fatalf("replacement after discard should not reuse discarded terminal")
	}
}

func TestManagerOpenChecksSessionLimitBeforeStartingPTY(t *testing.T) {
	manager := NewManager()
	for i := 0; i < maxSessionsPerOwnerAndAgent; i++ {
		session := &Session{
			id:          newTerminalID(int64(i + 1)),
			ownerKey:    "owner-a",
			agentKey:    "coder",
			terminalKey: "existing",
		}
		manager.sessions[session.ID()] = session
	}
	_, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         t.TempDir(),
		Shell:       "/definitely/missing/shell",
	})
	if err == nil || !strings.Contains(err.Error(), ErrSessionLimit.Error()) {
		t.Fatalf("expected session limit before shell start, got %v", err)
	}
}

func TestManagerOpen_ignoresFinishedSessionAwaitingRemoval(t *testing.T) {
	manager := NewManager()
	workspace := t.TempDir()
	stale := &Session{
		id:          "term_stale",
		ownerKey:    "owner-a",
		agentKey:    "coder",
		terminalKey: "main",
		cwd:         workspace,
		shell:       "/bin/sh",
	}
	stale.finished.Store(true)
	registryKey := agentSessionKey(stale.OwnerKey(), stale.AgentKey(), stale.TerminalKey())
	manager.sessions[stale.ID()] = stale
	manager.agentSessions[registryKey] = stale.ID()

	result, err := manager.Open(OpenRequest{
		OwnerKey:    "owner-a",
		AgentKey:    "coder",
		TerminalKey: "main",
		CWD:         workspace,
		Shell:       "/bin/sh",
		Cols:        80,
		Rows:        24,
		Env:         []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	manager.Start(result.Session)
	defer result.Session.Close("closed")
	if result.Reused {
		t.Fatalf("finished session should not be reused")
	}
	if result.Session.ID() == stale.ID() {
		t.Fatalf("opened stale terminal id %q", stale.ID())
	}
	if manager.agentSessions[registryKey] != result.Session.ID() {
		t.Fatalf("registry points to %q, want %q", manager.agentSessions[registryKey], result.Session.ID())
	}
	if _, ok := manager.sessions[stale.ID()]; ok {
		t.Fatalf("stale session should be removed from manager registry")
	}
}

func waitForTerminalOutput(t *testing.T, events <-chan Event, needle string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var output strings.Builder
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("events closed before %q; output=%q", needle, output.String())
			}
			if event.Type == EventOutput {
				output.WriteString(event.Data)
				if strings.Contains(output.String(), needle) {
					return event
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output=%q", needle, output.String())
		}
	}
}

func waitForTerminalExit(t *testing.T, events <-chan Event) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Type == EventExit {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal exit")
		}
	}
}

func waitForTerminalListStatus(t *testing.T, manager *Manager, ownerKey string, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		infos := manager.List(ownerKey)
		if len(infos) == 1 && infos[0].Status == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal status %q; infos=%#v", want, manager.List(ownerKey))
}
