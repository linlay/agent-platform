//go:build linux || darwin

package resources

import (
	"os"
	"syscall"
)

func openDependency(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
