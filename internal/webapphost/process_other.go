//go:build !linux

package webapphost

import (
	"fmt"
	"os"
)

func rootOwned(os.FileInfo) bool { return false }
func ProtectPlatform() error     { return fmt.Errorf("WebApp hosting requires Linux Compose") }
