//go:build !windows

package processgroup

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const guardianCommand = "__process-guardian"

// StartGuarded keeps ownership alive after the host is killed. The guardian
// inherits the snapshot's flock, but the adapter and its children do not.
func StartGuarded(cmd *exec.Cmd, pin *os.File) (*Tree, error) {
	if pin == nil || len(cmd.ExtraFiles) != 0 {
		return nil, errors.New("invalid guarded process lease")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	cmd.Args = append([]string{executable, guardianCommand, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = executable
	cmd.ExtraFiles = []*os.File{reader, pin}
	tree, err := Start(cmd)
	if err != nil {
		writer.Close()
		return nil, err
	}
	tree.owner = writer
	return tree, nil
}

// RunGuardian is dispatched by the executable before normal application startup.
// Only inherited descriptors carry ownership; it never reads a PID from disk.
func RunGuardian() (bool, int) {
	if len(os.Args) < 2 || os.Args[1] != guardianCommand {
		return false, 0
	}
	if len(os.Args) < 3 {
		return true, 125
	}
	owner := os.NewFile(3, "owner")
	pin := os.NewFile(4, "snapshot-lease")
	defer owner.Close()
	defer pin.Close()
	info, err := owner.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return true, 125
	}
	if _, err := pin.Stat(); err != nil {
		return true, 125
	}
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	// An orphaned, stopped Unix job receives HUP/CONT. Keep the cleanup owner
	// alive; an installed handler is reset to default when the adapter execs.
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	defer signal.Stop(hangup)
	lost := make(chan struct{})
	go func() { io.Copy(io.Discard, owner); close(lost) }()
	select {
	case <-lost:
		return true, 125
	default:
	}
	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	tree, err := Start(cmd)
	if err != nil {
		return true, 125
	}
	// The leader is not reaped yet, so its PID cannot be reused while recording
	// the initial identity. Keep the lease if the kernel cannot be inspected.
	descendants, err := newDescendantTracker(cmd.Process.Pid)
	for err != nil {
		time.Sleep(100 * time.Millisecond)
		descendants, err = newDescendantTracker(cmd.Process.Pid)
	}
	// Only the adapter owns protocol descriptors. Retaining our copies would
	// mask its stdout EOF and leave pending RPCs alive after connection loss.
	os.Stdin.Close()
	os.Stdout.Close()
	os.Stderr.Close()
	done := make(chan struct{})
	var exitErr, cleanupErr error
	go func() { exitErr, cleanupErr = tree.Wait(); close(done) }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
watch:
	for {
		select {
		case <-lost:
			for {
				if _, err := descendants.terminate(); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			tree.Kill()
			<-done
			break watch
		case <-done:
			break watch
		case <-ticker.C:
			descendants.capture()
		}
	}
	// Retain the inherited lease until both the original group and recorded
	// detached descendants have disappeared. Inspection failures are not exit proof.
	for {
		clean, err := descendants.terminate()
		groupGone := cleanupErr == nil || errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH)
		if clean && err == nil && groupGone {
			break
		}
		if !groupGone {
			tree.Kill()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if exitErr != nil {
		return true, 1
	}
	return true, 0
}
