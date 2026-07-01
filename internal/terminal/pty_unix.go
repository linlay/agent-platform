//go:build !windows

package terminal

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type unixPTYProcess struct {
	file      *os.File
	cmd       *exec.Cmd
	shellPgrp int
}

func startPTY(req startPTYRequest) (ptyProcess, error) {
	cmd := exec.Command(req.Shell)
	cmd.Dir = req.CWD
	cmd.Env = append(os.Environ(), req.Env...)
	file, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(req.Rows),
		Cols: uint16(req.Cols),
	})
	if err != nil {
		return nil, err
	}
	shellPgrp := 0
	if cmd.Process != nil {
		if pgrp, pgrpErr := unix.Getpgid(cmd.Process.Pid); pgrpErr == nil {
			shellPgrp = pgrp
		}
	}
	return &unixPTYProcess{file: file, cmd: cmd, shellPgrp: shellPgrp}, nil
}

func (p *unixPTYProcess) Read(buf []byte) (int, error) {
	return p.file.Read(buf)
}

func (p *unixPTYProcess) Write(buf []byte) (int, error) {
	return p.file.Write(buf)
}

func (p *unixPTYProcess) Resize(cols int, rows int) error {
	return pty.Setsize(p.file, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
}

func (p *unixPTYProcess) Busy() bool {
	if p == nil || p.file == nil || p.shellPgrp <= 0 {
		return false
	}
	foregroundPgrp, err := unix.IoctlGetInt(int(p.file.Fd()), unix.TIOCGPGRP)
	if err != nil || foregroundPgrp <= 0 {
		return false
	}
	return foregroundPgrp != p.shellPgrp
}

func (p *unixPTYProcess) Close() error {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.file == nil {
		return nil
	}
	return p.file.Close()
}

func (p *unixPTYProcess) Wait() (*int, error) {
	if p.cmd == nil {
		return nil, nil
	}
	err := p.cmd.Wait()
	if p.cmd.ProcessState == nil {
		return nil, err
	}
	exitCode := p.cmd.ProcessState.ExitCode()
	return &exitCode, err
}
