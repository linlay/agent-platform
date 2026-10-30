//go:build windows

package runtimerecovery

import "golang.org/x/sys/windows"

func availableBytes(root string) (uint64, error) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
