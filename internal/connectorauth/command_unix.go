//go:build !windows

package connectorauth

import "os/exec"

func configureCommandLine(cmd *exec.Cmd) {}
