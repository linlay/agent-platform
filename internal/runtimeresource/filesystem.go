package runtimeresource

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func safeJoin(root, relative string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes runtime resource root: %s", relative)
	}
	return target, nil
}

func ensureRegularDirectory(directory string, mode fs.FileMode) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, mode); err != nil {
			return err
		}
		return os.Chmod(directory, mode)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("directory must not be a symlink or non-directory: %s", directory)
	}
	return os.Chmod(directory, mode)
}

func ensureRuntimeRoot(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		return os.Chmod(directory, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("runtime root must not be a symlink or non-directory: %s", directory)
	}
	return nil
}

func copyPath(source, target string) error {
	return copyPathWithPermissions(source, target, true)
}

func copyResourcePath(source, target string) error {
	return copyPathWithPermissions(source, target, false)
}

func copyPathWithPermissions(source, target string, private bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to copy symlink: %s", source)
	}
	if info.IsDir() {
		mode := info.Mode().Perm()
		if private {
			mode = 0o700
		}
		if err := os.MkdirAll(target, mode); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPathWithPermissions(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), private); err != nil {
				return err
			}
		}
		return os.Chmod(target, mode)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to copy unsupported file type: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	temporary := target + ".copying"
	targetFile, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(targetFile, sourceFile)
	closeErr := targetFile.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	mode := info.Mode().Perm()
	if private {
		mode = 0o600
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func copyPathIfExists(source, target string) (bool, error) {
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, copyPath(source, target)
}

func copyResourcePathIfExists(source, target string) (bool, error) {
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, copyResourcePath(source, target)
}

func atomicWriteJSON(filePath string, value any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filePath)
}

func removeEmptyParents(start, stop string) {
	current := filepath.Dir(start)
	stop = filepath.Clean(stop)
	for current != stop && strings.HasPrefix(current, stop+string(filepath.Separator)) {
		if err := os.Remove(current); err != nil {
			return
		}
		current = filepath.Dir(current)
	}
}
