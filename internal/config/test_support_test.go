package config

import "path/filepath"

func ProjectFile(relative string) string {
	return filepath.Join(projectRoot(), filepath.Clean(relative))
}
