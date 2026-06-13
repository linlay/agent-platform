package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"
)

type Canonical struct {
	Host  string
	Posix string
	Key   string
}

var caseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

func Canonicalize(path string) (Canonical, error) {
	host := filepath.Clean(ExpandHome(strings.TrimSpace(path)))
	if host == "" || host == "." {
		return Canonical{}, fmt.Errorf("resolve path: empty path")
	}
	if !filepath.IsAbs(host) {
		abs, err := filepath.Abs(host)
		if err != nil {
			return Canonical{}, fmt.Errorf("resolve path: %w", err)
		}
		host = abs
	}
	resolved, err := resolveExistingOrFuturePath(filepath.Clean(host))
	if err != nil {
		return Canonical{}, fmt.Errorf("resolve path: %w", err)
	}
	posix := filepath.ToSlash(filepath.Clean(resolved))
	return Canonical{
		Host:  filepath.Clean(resolved),
		Posix: posix,
		Key:   keyForPosix(posix),
	}, nil
}

func NearestExistingAncestor(path string) (Canonical, error) {
	canonical, err := Canonicalize(path)
	if err != nil {
		return Canonical{}, err
	}
	current := filepath.Clean(canonical.Host)
	for {
		if info, err := os.Stat(current); err == nil {
			if info.IsDir() {
				return Canonicalize(current)
			}
			return Canonicalize(filepath.Dir(current))
		}
		parent := filepath.Dir(current)
		if parent == current {
			return Canonicalize(current)
		}
		current = parent
	}
}

func WithinRoot(target, root Canonical) bool {
	targetKey := strings.TrimSpace(target.Key)
	rootKey := strings.TrimSpace(root.Key)
	if targetKey == "" || rootKey == "" {
		return false
	}
	if targetKey == rootKey {
		return true
	}
	if strings.HasSuffix(rootKey, "/") {
		return strings.HasPrefix(targetKey, rootKey)
	}
	return strings.HasPrefix(targetKey, rootKey+"/")
}

func ExpandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func resolveExistingOrFuturePath(path string) (string, error) {
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(evaluated), nil
	}
	existing := path
	missing := []string{}
	for {
		if existing == "" || existing == "." {
			break
		}
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	evaluatedParent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		if abs, absErr := filepath.Abs(path); absErr == nil {
			return filepath.Clean(abs), nil
		}
		return "", err
	}
	return filepath.Clean(filepath.Join(append([]string{evaluatedParent}, missing...)...)), nil
}

func keyForPosix(posix string) string {
	key := posix
	if caseInsensitive {
		key = strings.ToLower(key)
	}
	if runtime.GOOS == "darwin" {
		key = norm.NFC.String(key)
	}
	return key
}
