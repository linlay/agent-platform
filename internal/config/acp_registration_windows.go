//go:build windows

package config

import "golang.org/x/sys/windows"

func replaceACPSettings(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// Replace without a delete-then-rename gap; an open incompatible handle fails
	// the request and retains the original settings for retry.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
