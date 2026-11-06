package connector

import (
	"agent-platform/internal/runtimefs"
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
	p, err := runtimefs.LockPath(filepath.Dir(root), "connectors", "operations", id+".lock")
	if err != nil {
		return nil, err
	}
	return acquireOperationFile(p)
}

func acquireOperationFile(p string) (func(), error) {
	f, err := runtimefs.OpenLockFile(p)
	if err != nil {
		return nil, err
	}
	ok, err := runtimefs.TryExclusive(f)
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
func openVersionLease(dir string) (*os.File, error) {
	id := filepath.Base(filepath.Dir(dir))
	if !ValidID(id) || !validDigest(filepath.Base(dir)) {
		return nil, fmt.Errorf("invalid shared connector version")
	}
	return runtimefs.OpenLock(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "connectors", "leases", id, filepath.Base(dir)+".lock")
}
