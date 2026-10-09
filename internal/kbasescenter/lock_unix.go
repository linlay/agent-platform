//go:build !windows

package kbasescenter

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockWorker(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
