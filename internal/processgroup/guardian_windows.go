//go:build windows

package processgroup

import (
	"os"
	"os/exec"
)

// Windows closes the owning process's kill-on-close Job handle on host death.
func StartGuarded(cmd *exec.Cmd, _ *os.File) (*Tree, error) { return Start(cmd) }
func RunGuardian() (bool, int)                              { return false, 0 }
