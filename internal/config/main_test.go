package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Legacy Load() tests use a disposable project root, never a developer's real configs.
func TestMain(m *testing.M) {
	source := projectRoot()
	root, err := os.MkdirTemp("", "platform-config-tests-")
	if err != nil {
		panic(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "configs"), 0700); err != nil {
		panic(err)
	}
	files, err := filepath.Glob(filepath.Join(source, "configs", "*.example.*"))
	if err != nil {
		panic(err)
	}
	files = append(files, filepath.Join(source, ".env.example"))
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			panic(err)
		}
		if err = os.WriteFile(filepath.Join(root, rel), b, 0600); err != nil {
			panic(err)
		}
	}
	projectRootDir = root
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
