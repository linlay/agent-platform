package connector

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchiveRenamePlatformPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		code           syscall.Errno
		fail           int
		wantCalls      int
		wantError      bool
	}{
		{"windows access recovers", "windows", 5, 2, 3, false},
		{"windows sharing recovers", "windows", 32, 1, 2, false},
		{"windows lock recovers", "windows", 33, 1, 2, false},
		{"windows exhausted", "windows", 5, 99, 6, true},
		{"windows missing", "windows", 2, 99, 1, true},
		{"windows existing", "windows", 183, 99, 1, true},
		{"macOS permission", "darwin", 13, 99, 1, true},
		{"macOS same numeric code", "darwin", 5, 99, 1, true},
		{"linux permission", "linux", 13, 99, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var delays []time.Duration
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			err := retryArchiveRename(context.Background(), tc.platform, "publish", "stage/demo", "root/demo", func(from, to string) error {
				calls++
				if from != "stage/demo" || to != "root/demo" {
					t.Fatal("paths changed")
				}
				if calls <= tc.fail {
					return &os.LinkError{Op: "rename", Old: from, New: to, Err: tc.code}
				}
				return nil
			}, func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil })
			if (err != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err != nil {
				if !errors.Is(err, tc.code) {
					t.Fatalf("lost errno: %v", err)
				}
				for _, field := range []string{"phase=publish", "platform=" + tc.platform, "system_code=", "attempts=", "retries=", "elapsed_ms=", "source=", "target="} {
					if !strings.Contains(err.Error(), field) {
						t.Fatalf("missing %s: %v", field, err)
					}
				}
			} else if calls > 1 && !strings.Contains(logs.String(), "rename recovered") {
				t.Fatal("missing recovery log")
			}
			if tc.name == "windows exhausted" {
				total := time.Duration(0)
				for _, delay := range delays {
					total += delay
				}
				if total != 2500*time.Millisecond || !strings.Contains(err.Error(), "win32:5/0x00000005 (ERROR_ACCESS_DENIED)") {
					t.Fatalf("budget=%v err=%v", total, err)
				}
			}
		})
	}
}

func TestArchiveRenameCancellationPreservesSystemError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryArchiveRename(ctx, "windows", "publish", "source", "target", func(string, string) error { calls++; return syscall.Errno(32) }, func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() })
	if calls != 1 || !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.Errno(32)) || !strings.Contains(err.Error(), "ERROR_SHARING_VIOLATION") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	err = retryArchiveRename(ctx, "windows", "publish", "source", "target", func(string, string) error { calls++; return nil }, waitArchiveRename)
	if calls != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation: %d %v", calls, err)
	}
	// Rollback must remain possible after the HTTP request is canceled.
	err = retryArchiveRename(context.WithoutCancel(ctx), "windows", "restore_previous", "backup", "target", func(string, string) error { calls++; return nil }, waitArchiveRename)
	if err != nil || calls != 1 {
		t.Fatalf("rollback: %d %v", calls, err)
	}
	if err = waitArchiveRename(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
