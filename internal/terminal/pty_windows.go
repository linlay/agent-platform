//go:build windows

package terminal

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPTYProcess struct {
	console windows.Handle
	input   *os.File
	output  *os.File
	process windows.Handle
	thread  windows.Handle

	mu          sync.Mutex
	closeOnce   sync.Once
	consoleOnce sync.Once
	waitOnce    sync.Once
	waitCode    *int
	waitErr     error
}

func startPTY(req startPTYRequest) (ptyProcess, error) {
	var inputRead, inputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return nil, err
	}
	var outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		_ = windows.CloseHandle(inputRead)
		_ = windows.CloseHandle(inputWrite)
		return nil, err
	}

	var console windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{
		X: int16(req.Cols),
		Y: int16(req.Rows),
	}, inputRead, outputWrite, 0, &console)
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		if isUnsupportedPseudoConsoleError(err) {
			return nil, ErrUnsupported
		}
		return nil, err
	}

	proc := &windowsPTYProcess{
		console: console,
		input:   os.NewFile(uintptr(inputWrite), "conpty-input"),
		output:  os.NewFile(uintptr(outputRead), "conpty-output"),
	}
	if proc.input == nil || proc.output == nil {
		proc.Close()
		return nil, fmt.Errorf("create conpty pipe files")
	}
	if err := proc.startShell(req); err != nil {
		proc.Close()
		if isUnsupportedPseudoConsoleError(err) {
			return nil, ErrUnsupported
		}
		return nil, err
	}
	return proc, nil
}

func (p *windowsPTYProcess) startShell(req startPTYRequest) error {
	attributeList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attributeList.Delete()

	console := p.console
	if err := attributeList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&console),
		unsafe.Sizeof(console),
	); err != nil {
		return err
	}

	startupInfo := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
		},
		ProcThreadAttributeList: attributeList.List(),
	}

	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{req.Shell}))
	if err != nil {
		return err
	}
	var cwd *uint16
	if strings.TrimSpace(req.CWD) != "" {
		cwd, err = windows.UTF16PtrFromString(req.CWD)
		if err != nil {
			return err
		}
	}
	env, err := windowsEnvBlock(append(os.Environ(), req.Env...))
	if err != nil {
		return err
	}
	var envPtr *uint16
	if len(env) > 0 {
		envPtr = &env[0]
	}

	processInfo := new(windows.ProcessInformation)
	err = windows.CreateProcess(
		nil,
		commandLine,
		nil,
		nil,
		false,
		windows.CREATE_DEFAULT_ERROR_MODE|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT,
		envPtr,
		cwd,
		&startupInfo.StartupInfo,
		processInfo,
	)
	if err != nil {
		return err
	}
	p.process = processInfo.Process
	p.thread = processInfo.Thread
	go p.closeConsoleAfterExit(processInfo.Process)
	return nil
}

func (p *windowsPTYProcess) Read(buf []byte) (int, error) {
	if p == nil || p.output == nil {
		return 0, os.ErrClosed
	}
	return p.output.Read(buf)
}

func (p *windowsPTYProcess) Write(buf []byte) (int, error) {
	if p == nil || p.input == nil {
		return 0, os.ErrClosed
	}
	return p.input.Write(buf)
}

func (p *windowsPTYProcess) Resize(cols int, rows int) error {
	if p == nil {
		return ErrNotFound
	}
	p.mu.Lock()
	console := p.console
	p.mu.Unlock()
	if console == 0 {
		return ErrNotFound
	}
	err := windows.ResizePseudoConsole(console, windows.Coord{
		X: int16(cols),
		Y: int16(rows),
	})
	if err != nil && isUnsupportedPseudoConsoleError(err) {
		return ErrUnsupported
	}
	return err
}

func (p *windowsPTYProcess) Busy() bool {
	return false
}

func (p *windowsPTYProcess) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.closeOnce.Do(func() {
		if p.process != 0 {
			_ = windows.TerminateProcess(p.process, 1)
		}
		if p.input != nil {
			err = errors.Join(err, p.input.Close())
		}
		if p.output != nil {
			err = errors.Join(err, p.output.Close())
		}
		p.closeConsole()
	})
	return err
}

func (p *windowsPTYProcess) Wait() (*int, error) {
	if p == nil {
		return nil, nil
	}
	p.waitOnce.Do(func() {
		if p.process == 0 {
			return
		}
		_, waitErr := windows.WaitForSingleObject(p.process, windows.INFINITE)
		if waitErr != nil {
			p.waitErr = waitErr
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
			p.waitErr = err
			return
		}
		exitCode := int(code)
		p.waitCode = &exitCode
		if p.thread != 0 {
			_ = windows.CloseHandle(p.thread)
			p.thread = 0
		}
		_ = windows.CloseHandle(p.process)
		p.process = 0
	})
	return p.waitCode, p.waitErr
}

func (p *windowsPTYProcess) closeConsoleAfterExit(process windows.Handle) {
	if process == 0 {
		return
	}
	_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
	p.closeConsole()
}

func (p *windowsPTYProcess) closeConsole() {
	if p == nil {
		return
	}
	p.consoleOnce.Do(func() {
		p.mu.Lock()
		console := p.console
		p.console = 0
		p.mu.Unlock()
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
	})
}

func windowsEnvBlock(env []string) ([]uint16, error) {
	if len(env) == 0 {
		return []uint16{0, 0}, nil
	}
	env = append([]string(nil), env...)
	sort.SliceStable(env, func(i, j int) bool {
		return strings.ToUpper(env[i]) < strings.ToUpper(env[j])
	})

	out := make([]uint16, 0, len(env)*16+1)
	for _, item := range env {
		if strings.ContainsRune(item, 0) {
			return nil, fmt.Errorf("environment contains NUL")
		}
		out = append(out, utf16.Encode([]rune(item))...)
		out = append(out, 0)
	}
	out = append(out, 0)
	return out, nil
}

func isUnsupportedPseudoConsoleError(err error) bool {
	return errors.Is(err, windows.ERROR_PROC_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED)
}
