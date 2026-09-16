package connector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Only Windows sharing/access failures get a bounded retry. In particular,
// macOS permission errors and missing paths must fail immediately.
func renameArchiveDirectory(ctx context.Context, phase, source, target string) error {
	return retryArchiveRename(ctx, runtime.GOOS, phase, source, target, os.Rename, waitArchiveRename)
}

func waitArchiveRename(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func archiveSystemError(goos string, err error) (string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "unavailable", false
	}
	if goos == "windows" {
		name := "UNKNOWN_WIN32_ERROR"
		retry := false
		switch uint64(errno) {
		case 5:
			name, retry = "ERROR_ACCESS_DENIED", true
		case 32:
			name, retry = "ERROR_SHARING_VIOLATION", true
		case 33:
			name, retry = "ERROR_LOCK_VIOLATION", true
		}
		return fmt.Sprintf("win32:%d/0x%08X (%s)", uint64(errno), uint64(errno), name), retry
	}
	return fmt.Sprintf("errno:%d (%s)", uint64(errno), errno.Error()), false
}

func retryArchiveRename(ctx context.Context, goos, phase, source, target string, rename func(string, string) error, wait func(context.Context, time.Duration) error) error {
	delays := [...]time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second}
	started := time.Now()
	attempts := 0
	var last error
	systemCode := "unavailable"
	finish := func(cause error, outcome string) error {
		// The message is also displayed by the existing WebClient error-details UI.
		detail := fmt.Errorf("connector import rename failed: phase=%s; platform=%s; system_code=%s; attempts=%d; retries=%d; elapsed_ms=%d; outcome=%s; source=%q; target=%q: %w", phase, goos, systemCode, attempts, max(0, attempts-1), time.Since(started).Milliseconds(), outcome, source, target, cause)
		log.Printf("[connector-import] %v", detail)
		return detail
	}
	for {
		if err := ctx.Err(); err != nil {
			return finish(errors.Join(last, err), "canceled")
		}
		attempts++
		err := rename(source, target)
		if err == nil {
			if attempts > 1 {
				log.Printf("[connector-import] rename recovered: phase=%s platform=%s last_system_code=%s attempts=%d retries=%d elapsed_ms=%d source=%q target=%q", phase, goos, systemCode, attempts, attempts-1, time.Since(started).Milliseconds(), source, target)
			}
			return nil
		}
		last = err
		var retry bool
		systemCode, retry = archiveSystemError(goos, err)
		if !retry {
			return finish(err, "not_retryable")
		}
		if attempts > len(delays) {
			return finish(err, "retry_exhausted")
		}
		delay := delays[attempts-1]
		log.Printf("[connector-import] rename retry: phase=%s platform=%s system_code=%s attempt=%d next_delay_ms=%d elapsed_ms=%d source=%q target=%q error=%q", phase, goos, systemCode, attempts, delay.Milliseconds(), time.Since(started).Milliseconds(), source, target, err.Error())
		if err := wait(ctx, delay); err != nil {
			return finish(errors.Join(last, err), "canceled")
		}
	}
}
