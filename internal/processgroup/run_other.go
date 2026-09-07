//go:build !windows

package processgroup

import "os/exec"

func Run(cmd *exec.Cmd) error { return cmd.Run() }
