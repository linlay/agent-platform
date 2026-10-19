//go:build !windows

package connector

import "os"

func unsafeDirectoryAlias(_ string, info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
