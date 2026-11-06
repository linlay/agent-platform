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
	"time"
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

// Tree owns a suspended-start Job Object and all of its child processes.
type Tree struct {
	cmd *exec.Cmd
	job *Job
}

func Run(cmd *exec.Cmd) error {
	tree, err := Start(cmd)
	if err != nil {
		return err
	}
	// Ordinary commands do not retain a snapshot lease on cleanup failure.
	defer tree.job.Close()
	exitErr, cleanupErr := tree.Wait()
	return errors.Join(exitErr, cleanupErr)
}

// Start starts suspended, assigns the job, then resumes the initial thread. This
// closes the race where Bash could spawn an untracked child before assignment.
// os/exec continues to own the ordinary pipes and WaitDelay/capture behavior.
func Start(cmd *exec.Cmd) (*Tree, error) {
	job, err := NewJob()
	if err != nil {
		return nil, fmt.Errorf("create command job: %w", err)
	}
	started := false
	defer func() {
		if !started {
			job.Close()
		}
	}()
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
		return nil, err
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
		return nil, fmt.Errorf("start managed command tree: %w", err)
	}
	started = true
	return &Tree{cmd: cmd, job: job}, nil
}

func (t *Tree) Kill() error { return t.job.Terminate() }

func (t *Tree) Wait() (exitErr, cleanupErr error) {
	exitErr = t.cmd.Wait()
	if err := t.job.Terminate(); err != nil {
		return exitErr, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (four LARGE_INTEGER, four DWORD).
		var accounting struct {
			TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
			TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
		}
		t.job.mu.Lock()
		err := windows.QueryInformationJobObject(t.job.handle, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		t.job.mu.Unlock()
		if err != nil {
			return exitErr, err
		}
		if accounting.ActiveProcesses == 0 {
			return exitErr, t.job.Close()
		}
		if time.Now().After(deadline) {
			return exitErr, errors.New("process tree did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
