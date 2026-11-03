//go:build linux

package webapphost

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func rootOwned(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == 0
}

// ProtectPlatform is called only for the explicitly enabled Compose host.
func ProtectPlatform() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("protect WebApp host process: %w", err)
	}
	return nil
}
