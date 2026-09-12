package connectorauth

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// cmd.exe parses /c as shell text, unlike the CommandLineToArgvW convention
// used by os/exec. Preserve the command text instead of backslash-escaping it.
func configureCommandLine(cmd *exec.Cmd) {
	if !strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") || len(cmd.Args) != 5 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + " /d /s /c " + cmd.Args[4]
}
