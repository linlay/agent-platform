//go:build !windows

package subprocess

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureBackgroundLeavesUnixAttributesAndCancellation(t *testing.T) {
	cmd := exec.Command("unused")
	ConfigureBackground(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatal("added process attributes on macOS/Linux")
	}
	original := &syscall.SysProcAttr{}
	cmd.SysProcAttr = original
	canceled := false
	cmd.Cancel = func() error { canceled = true; return nil }
	ConfigureBackground(cmd)
	if cmd.SysProcAttr != original {
		t.Fatal("replaced existing macOS/Linux process attributes")
	}
	_ = cmd.Cancel()
	if !canceled {
		t.Fatal("cancellation callback changed")
	}
}
