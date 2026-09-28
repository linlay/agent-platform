package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrBusy = errors.New("connector preparation or mutation is in progress")

// AcquireOperation serializes CLI jobs with source mutations, including the
// standalone management process. root is the connector source directory under
// the runtime root. The OS releases the lock after a crash.
func AcquireOperation(root, id string) (func(), error) {
	if !ValidID(id) || root == "" {
		return nil, fmt.Errorf("invalid connector operation")
	}
	p, err := runtimeLockPath(filepath.Dir(root), "connectors", "operations", id+".lock")
	if err != nil {
		return nil, err
	}
	return acquireOperationFile(p)
}

func openLockFile(p string) (*os.File, error) {
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid connector operation lock")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
}

func acquireOperationFile(p string) (func(), error) {
	f, err := openLockFile(p)
	if err != nil {
		return nil, err
	}
	ok, err := tryOperationLock(f)
	if err != nil || !ok {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrBusy
	}
	return func() { f.Close() }, nil
}

// runtimeLockPath keeps lock objects outside generated packages. Every directory
// below the runtime root must be real so distinct lock scopes cannot alias.
func runtimeLockPath(runtimeRoot string, parts ...string) (string, error) {
	if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
		return "", err
	}
	dir := runtimeRoot
	directories := append([]string{".lock"}, parts[:len(parts)-1]...)
	for _, part := range directories {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime lock directory must be a real directory: %s", dir)
		}
	}
	return filepath.Join(dir, parts[len(parts)-1]), nil
}

func openRuntimeLock(runtimeRoot string, parts ...string) (*os.File, error) {
	p, err := runtimeLockPath(runtimeRoot, parts...)
	if err != nil {
		return nil, err
	}
	return openLockFile(p)
}

func openVersionLease(dir string) (*os.File, error) {
	id := filepath.Base(filepath.Dir(dir))
	if !ValidID(id) || !validDigest(filepath.Base(dir)) {
		return nil, fmt.Errorf("invalid shared connector version")
	}
	return openRuntimeLock(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "connectors", "leases", id, filepath.Base(dir)+".lock")
}
