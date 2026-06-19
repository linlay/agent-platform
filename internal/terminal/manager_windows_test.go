//go:build windows

package terminal

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManagerRunsInteractivePTYOnWindows(t *testing.T) {
	manager := NewManager()
	session, err := manager.Open(OpenRequest{
		AgentKey: "coder",
		CWD:      t.TempDir(),
		Shell:    "powershell.exe",
		Cols:     80,
		Rows:     24,
	})
	if errors.Is(err, ErrUnsupported) {
		t.Skip("Windows ConPTY is unsupported on this host")
	}
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	manager.Start(session)
	defer session.Close("closed")

	if err := manager.Input(session.ID(), "Write-Output terminal-ready\r\nexit\r\n"); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(10 * time.Second)
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
