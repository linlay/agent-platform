//go:build !windows

package terminal

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type unixPTYProcess struct {
	file *os.File
	cmd  *exec.Cmd
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
	return &unixPTYProcess{file: file, cmd: cmd}, nil
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
