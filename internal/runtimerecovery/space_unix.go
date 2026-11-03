//go:build !windows

package runtimerecovery

import "syscall"

func availableBytes(root string) (uint64, error) {
	var info syscall.Statfs_t
	if err := syscall.Statfs(root, &info); err != nil {
		return 0, err
	}
	return info.Bavail * uint64(info.Bsize), nil
}
