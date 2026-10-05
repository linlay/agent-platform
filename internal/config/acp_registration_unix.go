//go:build !windows

package config

import "os"

func replaceACPSettings(source, target string) error {
	return os.Rename(source, target)
}
