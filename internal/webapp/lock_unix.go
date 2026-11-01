//go:build !windows

package webapp

import (
	"golang.org/x/sys/unix"
	"os"
)

func acquireInstance(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func availableBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	err := unix.Statfs(path, &st)
	return st.Bavail * uint64(st.Bsize), err
}
