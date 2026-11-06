//go:build !windows

package runtimefs

import (
	"os"
	"syscall"
)

func PrivateOwned(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return os.ErrPermission
	}
	return nil
}
