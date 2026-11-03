//go:build !linux

package webapphost

import (
	"fmt"
	"os"
)

func lockGeneration(string) (*os.File, error) { return nil, fmt.Errorf("Linux Compose only") }
func ownsLoopbackPort(int, string) bool       { return false }
func processTreeRSS(int) uint64               { return 0 }
