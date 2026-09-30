package connectorauth

import (
	"os"
	"os/exec"
	"strconv"

	"agent-platform/internal/subprocess"
)

func configureProcess(cmd *exec.Cmd) {
	subprocess.ConfigureBackground(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		subprocess.ConfigureBackground(kill)
		return kill.Run()
	}
}
