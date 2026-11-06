//go:build !windows

package processgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Tree owns a process group, including children that outlive its leader.
// It is a lifecycle boundary, not a sandbox against deliberate setsid escapes.
type Tree struct {
	cmd   *exec.Cmd
	owner *os.File
}

func Start(cmd *exec.Cmd) (*Tree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Tree{cmd: cmd}, nil
}
func (t *Tree) Kill() error {
	if t.owner != nil {
		err := t.owner.Close()
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		return err
	}
	err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Wait returns only after the leader and all remaining group members have exited.
func (t *Tree) Wait() (exitErr, cleanupErr error) {
	exitErr = t.cmd.Wait()
	if t.owner != nil {
		t.owner.Close()
		// 0/1 confirm tree cleanup; 125 fails before spawning. A guardian
		// runtime abort (e.g. Go's exit 2) is not proof its children exited.
		code := t.cmd.ProcessState.ExitCode()
		if code != 0 && code != 1 && code != 125 {
			return exitErr, errors.New("process guardian exited before confirming cleanup")
		}
		return exitErr, nil
	}
	killErr := t.Kill()
	// Darwin can report EPERM while the remaining group is being reaped. A
	// failed signal is not proof of a live tree; only ESRCH confirms cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-t.cmd.Process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return exitErr, nil
		}
		if time.Now().After(deadline) {
			return exitErr, errors.Join(killErr, errors.New("process tree did not exit"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
