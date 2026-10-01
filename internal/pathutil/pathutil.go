package pathutil

import (
	"errors"
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
	host := ExpandHome(strings.TrimSpace(path))
	if host == "" || host == "." {
		return Canonical{}, fmt.Errorf("resolve path: empty path")
	}
	if !filepath.IsAbs(host) {
		cwd, err := os.Getwd()
		if err != nil {
			return Canonical{}, fmt.Errorf("resolve path: %w", err)
		}
		host = JoinUnclean(cwd, host)
	}
	resolved, err := resolveExistingOrFuturePath(host)
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

// IsFilesystemRoot reports whether path resolves to the host filesystem,
// volume, or share root. Callers use this for operations whose cost or scope
// makes searching an entire root unsafe even when ordinary path access is
// otherwise allowed.
func IsFilesystemRoot(path string) bool {
	canonical, err := Canonicalize(path)
	if err != nil {
		return false
	}
	return IsCanonicalFilesystemRoot(canonical.Host)
}

// IsCanonicalFilesystemRoot is the non-resolving form of IsFilesystemRoot.
// The input must already be an absolute canonical host path.
func IsCanonicalFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	return filepath.IsAbs(cleaned) && filepath.Dir(cleaned) == cleaned
}

func ExpandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if path == "~" {
				return home
			}
			return JoinUnclean(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func resolveExistingOrFuturePath(path string) (string, error) {
	return resolvePhysicalPath(path, 0)
}

// JoinUnclean preserves parent components until after symlink resolution.
// filepath.Join/Clean before resolution changes link/../file semantics.
func JoinUnclean(base, relative string) string {
	return strings.TrimRight(base, string(filepath.Separator)) + string(filepath.Separator) + relative
}

func resolvePhysicalPath(path string, links int) (string, error) {
	if links > 40 {
		return "", fmt.Errorf("too many symbolic links")
	}
	path = filepath.FromSlash(path)
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, volume), string(filepath.Separator))
	missing := false
	for i, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if missing {
				return "", fmt.Errorf("parent traversal through nonexistent directory")
			}
			current = filepath.Dir(current)
			continue
		}
		current = filepath.Join(current, part)
		if missing {
			continue
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return "", err
		}
		if isLinkLike(current, info) {
			target, err := os.Readlink(current)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = JoinUnclean(filepath.Dir(current), target)
			}
			return resolvePhysicalPath(JoinUnclean(target, strings.Join(parts[i+1:], string(filepath.Separator))), links+1)
		}
		if !info.IsDir() && i < len(parts)-1 && strings.Join(parts[i+1:], "") != "" {
			return "", fmt.Errorf("path component is not a directory: %s", current)
		}
	}
	return current, nil
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

// isLinkLike reports symlinks and, on Windows, junctions and other mount-point
// reparse points. Since Go 1.23 os.Lstat reports those as ModeIrregular rather
// than ModeSymlink, but os.Readlink still resolves their target.
func isLinkLike(path string, info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if runtime.GOOS != "windows" || info.Mode()&os.ModeIrregular == 0 {
		return false
	}
	_, err := os.Readlink(path)
	return err == nil
}
