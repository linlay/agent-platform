package runtimefs

import (
	"fmt"
	"os"
	"path/filepath"
)

func OpenLockFile(p string) (*os.File, error) {
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid runtime lock")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
}

func LockPath(runtimeRoot string, parts ...string) (string, error) {
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

func OpenLock(runtimeRoot string, parts ...string) (*os.File, error) {
	p, err := LockPath(runtimeRoot, parts...)
	if err != nil {
		return nil, err
	}
	return OpenLockFile(p)
}
