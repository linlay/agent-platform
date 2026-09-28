//go:build !windows

package connectorauth

import "os/exec"

func configureCommandLine(_ *exec.Cmd) {}
