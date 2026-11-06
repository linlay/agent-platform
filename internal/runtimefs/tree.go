package runtimefs

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Digest(root string) ([32]byte, error) {
	if err := ValidateTree(root); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Mode()&(os.ModeType|0o777))
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := linkInTree(root, path)
			if err != nil {
				return err
			}
			fmt.Fprint(h, target)
		} else if !entry.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		fmt.Fprint(h, "\x00")
		return nil
	})
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, err
}

func linkInTree(root, path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Rel(canonical, target)
}

func CopyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destination, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := linkInTree(source, path)
			if err != nil {
				return err
			}
			link, err = filepath.Rel(filepath.Dir(destination), filepath.Join(target, link))
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if err := output.Chmod(info.Mode().Perm()); err != nil {
			output.Close()
			return err
		}
		_, err = io.Copy(output, input)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func ValidateTree(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("package root must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(canonical, target)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("symlink escapes package: %s", path)
			}
		} else if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported package file: %s", path)
		}
		return nil
	})
}

// Publish installs an already validated candidate without replacing a published tree.
// The caller holds its namespace's assembly/installation lock.
func Publish(stage, target string) error {
	expected, err := Digest(stage)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		actual, err := Digest(target)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("published runtime integrity failure")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(stage, target)
}

// RemoveUnused uses an OS lease and verifies the content address before removal.
// Domain callers first exclude current references and hold their assembly lock.
func RemoveUnused(dir string, lock *os.File) error {
	ok, err := TryExclusive(lock)
	if err != nil || !ok {
		return err
	}
	actual, err := Digest(dir)
	if err != nil {
		return nil // Preserve unknown/corrupt trees; they do not establish authority.
	}
	if fmt.Sprintf("%x", actual) != filepath.Base(dir) {
		return nil
	}
	return os.RemoveAll(dir)
}
