//go:build windows

package terminal

import (
	"errors"
	"testing"
)

func TestManagerReturnsUnsupportedOnWindows(t *testing.T) {
	manager := NewManager()
	_, err := manager.Open(OpenRequest{
		AgentKey: "coder",
		CWD:      `C:\`,
		Shell:    "powershell.exe",
		Cols:     80,
		Rows:     24,
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported, got %v", err)
	}
}
