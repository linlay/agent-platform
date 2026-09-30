package subprocess

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ConfigureBackground prevents a background console command from allocating a
// console window on Windows. It does not change pipes, arguments or cancellation.
func ConfigureBackground(cmd *exec.Cmd) {
	attr := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	// HideWindow only sets STARTUPINFO/SW_HIDE; CREATE_NO_WINDOW also prevents
	// console allocation for cmd.exe, node.exe and native console programs.
	attr.HideWindow = true
	attr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = &attr
}
