package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRuntimeAgentRenameWindowsDirectoryHandle(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(handle)
		}
	}()
	sleeps := 0
	err = renameRuntimeAgent("publish", source, target, "windows", os.Rename, func(time.Duration) {
		sleeps++
		if !closed {
			if closeErr := windows.CloseHandle(handle); closeErr != nil {
				t.Fatal(closeErr)
			}
			closed = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if sleeps != 1 {
		t.Fatalf("expected one retry after directory handle released, sleeps=%d", sleeps)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}
