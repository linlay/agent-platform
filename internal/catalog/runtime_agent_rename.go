package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Retry only Windows access/sharing/lock errors: freshly copied packages may
// briefly be held by scanners. macOS and other Unix platforms fail immediately.
// Never remove a destination that another writer recreated during publication.
func renameRuntimeAgent(stage, source, target, goos string, rename func(string, string) error, sleep func(time.Duration)) error {
	started := time.Now()
	attempts := 0
	var err error
	for {
		if _, statErr := os.Lstat(target); statErr == nil {
			err = fmt.Errorf("destination already exists; refusing replacement: %w", os.ErrExist)
			break
		} else if !os.IsNotExist(statErr) {
			err = fmt.Errorf("inspect destination: %w", statErr)
			break
		}
		attempts++
		err = rename(source, target)
		if err == nil {
			return nil
		}
		if !runtimeAgentRenameRetryable(goos, err) || attempts >= 6 {
			break
		}
		sleep(50 * time.Millisecond * time.Duration(1<<(attempts-1)))
	}
	var errno syscall.Errno
	code := "unavailable"
	if errors.As(err, &errno) {
		code = fmt.Sprintf("%d", uintptr(errno))
	}
	return fmt.Errorf("runtime agent directory rename failed: stage=%s platform=%s pid=%d attempts=%d elapsed_ms=%d os_error_code=%s source=%q target=%q source_state={%s} target_state={%s} source_parent_state={%s} target_parent_state={%s}: %w",
		stage, goos, os.Getpid(), attempts, time.Since(started).Milliseconds(), code,
		source, target, runtimeAgentPathState(source), runtimeAgentPathState(target),
		runtimeAgentPathState(filepath.Dir(source)), runtimeAgentPathState(filepath.Dir(target)), err)
}

func runtimeAgentRenameRetryable(goos string, err error) bool {
	switch goos {
	case "windows":
		var code syscall.Errno
		if !errors.As(err, &code) {
			return false
		}
		// Win32 ERROR_ACCESS_DENIED, ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION.
		return code == 5 || code == 32 || code == 33
	case "darwin":
		return false
	default:
		return false
	}
}

// Metadata only: do not enumerate package files or read configuration/secrets.
func runtimeAgentPathState(path string) string {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "exists=false"
	}
	if err != nil {
		return fmt.Sprintf("stat_error=%q", err)
	}
	return fmt.Sprintf("exists=true mode=%s is_dir=%t", info.Mode(), info.IsDir())
}
