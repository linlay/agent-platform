package connector

import (
	"os"
	"os/exec"
	"strconv"
)

func ConfigureProcessTree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
