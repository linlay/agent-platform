//go:build !windows

package runenv

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
