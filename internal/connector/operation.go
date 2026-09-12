package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrBusy = errors.New("connector preparation or mutation is in progress")

// AcquireOperation serializes CLI jobs with source mutations, including the
// standalone management process. The OS releases the lock after a crash.
func AcquireOperation(root, id string) (func(), error) {
	if !ValidID(id) || root == "" {
		return nil, fmt.Errorf("invalid connector operation")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	p := filepath.Join(root, ".cli-"+id+".lock")
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid connector operation lock")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
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
