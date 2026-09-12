//go:build !windows

package connector

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryOperationLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
