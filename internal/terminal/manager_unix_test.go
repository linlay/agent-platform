//go:build !windows

package terminal

import (
	"strings"
	"testing"
	"time"
)

func TestManagerRunsInteractivePTY(t *testing.T) {
	manager := NewManager()
	session, err := manager.Open(OpenRequest{
		AgentKey: "coder",
		CWD:      t.TempDir(),
		Shell:    "/bin/sh",
		Cols:     80,
		Rows:     24,
		Env:      []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	manager.Start(session)
	defer session.Close("closed")

	if err := manager.Input(session.ID(), "printf terminal-ready\\n\nexit\n"); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(5 * time.Second)
	var output strings.Builder
	for {
		select {
		case event, ok := <-session.Events():
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
