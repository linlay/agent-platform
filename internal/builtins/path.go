package builtins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var processBinState struct {
	sync.RWMutex
	dir string
}

func ConfigureProcessPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return configureProcessPathForExecutable(executable)
}

func configureProcessPathForExecutable(executable string) (string, error) {
	binaryDir := filepath.Dir(executable)
	candidates := []string{}
	if strings.EqualFold(filepath.Base(binaryDir), "backend") {
		candidates = append(candidates, filepath.Join(filepath.Dir(binaryDir), "bin"))
	}
	candidates = append(candidates, filepath.Join(binaryDir, "bin"))
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if !info.IsDir() {
			continue
		}
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		if err := os.Setenv("PATH", prependPath(candidate, os.Getenv("PATH"))); err != nil {
			return "", err
		}
		processBinState.Lock()
		processBinState.dir = candidate
		processBinState.Unlock()
		return candidate, nil
	}
	return "", nil
}

func EnsureBinInEnv(env []string) []string {
	processBinState.RLock()
	binDir := processBinState.dir
	processBinState.RUnlock()
	if binDir == "" {
		return append([]string(nil), env...)
	}
	pathValue := ""
	filtered := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			pathValue = value
			continue
		}
		filtered = append(filtered, item)
	}
	filtered = append(filtered, "PATH="+prependPath(binDir, pathValue))
	return filtered
}

// ResolveProcessBuiltin returns a locked builtin from the configured bundle
// bin directory. It deliberately does not consult PATH so a same-named user
// executable cannot replace a platform-distributed helper process.
func ResolveProcessBuiltin(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name {
		return "", errors.New("builtin name must be a filename")
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	processBinState.RLock()
	binDir := processBinState.dir
	processBinState.RUnlock()
	if binDir == "" {
		return "", os.ErrNotExist
	}
	path := filepath.Join(binDir, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("builtin path is a directory")
	}
	return path, nil
}

func prependPath(binDir string, current string) string {
	parts := filepath.SplitList(current)
	filtered := make([]string, 0, len(parts)+1)
	filtered = append(filtered, binDir)
	for _, part := range parts {
		if part == "" || samePath(part, binDir) {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, string(os.PathListSeparator))
}

func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
