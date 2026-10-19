package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// StateControl fences writes from an older installation without keeping any
// credentials. It and its OS lock live beside, never inside, the private state
// being removed. root is Sources.PersistentRoot(), including custom state roots.
type StateControl struct {
	Generation string `json:"generation,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	Deleting   bool   `json:"deleting,omitempty"`
}

// AcquireStateLock serializes credential writers with deletion. Acquiring the
// stable lock never creates a connector's private state directory.
func AcquireStateLock(ctx context.Context, root, id string) (func(), error) {
	path, err := stateControlPath(root, id, ".lock", true)
	if err != nil {
		return nil, err
	}
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		locked, err := tryOperationLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			var once sync.Once
			return func() { once.Do(func() { f.Close() }) }, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// ReadStateControl is read-only; a never-installed connector has zero control.
func ReadStateControl(root, id string) (StateControl, error) {
	path, err := stateControlPath(root, id, ".json", false)
	if err != nil {
		return StateControl{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return StateControl{}, nil
	}
	if err != nil {
		return StateControl{}, err
	}
	var control StateControl
	if err := DecodeJSON(data, &control); err != nil {
		return StateControl{}, fmt.Errorf("invalid connector state control: %w", err)
	}
	return control, nil
}

// WriteStateControl atomically persists non-secret lifecycle data. The caller
// must hold AcquireStateLock and must explicitly activate a re-imported package.
func WriteStateControl(root, id string, control StateControl) error {
	path, err := stateControlPath(root, id, ".json", true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(control)
	if err != nil {
		return err
	}
	return writeControlFile(path, data)
}

func stateControlPath(root, id, suffix string, create bool) (string, error) {
	if !ValidID(id) || strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("invalid connector state control root or id")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.Dir(root) == root {
		return "", fmt.Errorf("connector state control cannot use a filesystem root")
	}
	if err := realDirectory(root, create); err != nil {
		return "", err
	}
	dir := filepath.Join(root, ".lock")
	if err := realDirectory(dir, create); err != nil {
		return "", err
	}
	dir = filepath.Join(dir, id)
	if err := realDirectory(dir, create); err != nil {
		return "", err
	}
	var name string
	switch suffix {
	case ".lock":
		name = "state.lock"
	case ".json":
		name = "control.json"
	case ".delete.json":
		name = "delete.json"
	default:
		return "", fmt.Errorf("invalid connector state control file")
	}
	path := filepath.Join(dir, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || unsafeDirectoryAlias(path, info) {
			return "", fmt.Errorf("connector state control must be a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

// realDirectory rejects both symlinks and Windows reparse points. Children of
// each managed root are checked separately before creating or deleting them.
func realDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if !create {
			return nil
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || unsafeDirectoryAlias(path, info) {
		return fmt.Errorf("connector directory must be a real directory: %s", path)
	}
	return nil
}

func writeControlFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-control-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Windows does not support syncing directory handles through os.File.Sync.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
