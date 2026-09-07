//go:build windows

// Package processgroup owns Windows command trees without placing the Platform
// process itself in a job. It is lifecycle management, not a security sandbox.
package processgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Job struct {
	mu     sync.Mutex
	handle windows.Handle
}

func NewJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return &Job{handle: handle}, nil
}

func (j *Job) Assign(process windows.Handle) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return os.ErrClosed
	}
	return windows.AssignProcessToJobObject(j.handle, process)
}

func (j *Job) Terminate() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, 1)
}

func (j *Job) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(j.handle)
	j.handle = 0
	return err
}

// Run starts suspended, assigns the job, then resumes the initial thread. This
// closes the race where Bash could spawn an untracked child before assignment.
// os/exec continues to own the ordinary pipes and WaitDelay/capture behavior.
func Run(cmd *exec.Cmd) error {
	job, err := NewJob()
	if err != nil {
		return fmt.Errorf("create command job: %w", err)
	}
	defer job.Close()
	attr := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.CreationFlags |= windows.CREATE_SUSPENDED
	attr.HideWindow = true
	cmd.SysProcAttr = &attr
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			// Killing the primary as well covers cancellation before Assign.
			jobErr := job.Terminate()
			killErr := cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			return errors.Join(jobErr, killErr)
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = job.Assign(process)
		windows.CloseHandle(process)
	}
	if err == nil {
		err = resumeInitialThread(uint32(cmd.Process.Pid))
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = job.Terminate()
		_ = cmd.Wait()
		return fmt.Errorf("start managed command tree: %w", err)
	}
	return cmd.Wait()
}

func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return resumeErr
	}
	return fmt.Errorf("initial thread for process %d is unavailable: %w", pid, err)
}
