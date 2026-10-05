package memory

import (
	"errors"
	"os"
	"strings"
)

// PrepareSummary preserves an existing personal Markdown document once. The
// previous memory.md is retained as a backup, never read as a second source.
func (s *Store) PrepareSummary() error {
	root, err := os.OpenRoot(s.MemoryDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkFile(root, ".memory.lock"); err != nil {
		return err
	}
	lock, err := root.OpenFile(".memory.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = lockFile(lock); err != nil {
		return err
	}
	if err = checkFile(root, ".summary-migrated"); err != nil {
		return err
	}
	if _, err = root.Lstat(".summary-migrated"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Let memx recover its own journal after startup; never prevent the worker
	// from starting just because its last process was interrupted.
	if _, err = root.Lstat(".memx-pending.json"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := read(root, "summary.md", "memory", "")
	if err != nil {
		return err
	}
	if !d.Exists {
		old, err := read(root, "memory.md", "memory", "")
		if err != nil {
			return err
		}
		if old.Exists {
			if strings.ContainsRune(old.Content, 0) {
				return ErrInvalid
			}
			const temp = ".memory-migrate.tmp"
			if err = root.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer root.Remove(temp)
			_, err = f.WriteString(old.Content)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if err = root.Rename(temp, "summary.md"); err != nil {
				return err
			}
		}
	}
	marker, err := root.OpenFile(".summary-migrated", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Marker existence is sufficient and prevents deleting summary from ever
	// resurrecting a retained legacy backup on a subsequent startup.
	err = marker.Sync()
	closeErr := marker.Close()
	if err != nil {
		return err
	}
	return closeErr
}
