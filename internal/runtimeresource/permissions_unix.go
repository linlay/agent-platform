//go:build !windows

package runtimeresource

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func securePrivateTree(root string) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("private runtime resource state contains a symlink: %s", filePath)
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		}
		return os.Chmod(filePath, mode)
	})
}
