//go:build !windows

// Package subprocess contains process presentation policies, independent of
// command construction and process-tree lifecycle management.
package subprocess

import "os/exec"

// ConfigureBackground leaves macOS/Linux process attributes unchanged. macOS
// Dock presentation for Electron's Node runtime belongs to the Desktop launcher.
func ConfigureBackground(_ *exec.Cmd) {}
