package catalogorder

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// CopyLegacyDesktopPins is an offline management operation. Existing preferences
// of the target user always win, including an explicitly empty order list.
func CopyLegacyDesktopPins(centerDir, backupPath, subject string) (bool, error) {
	if !regexp.MustCompile(`^desktop-user:[a-f0-9]{64}$`).MatchString(subject) {
		return false, fmt.Errorf("verified Desktop subject is required")
	}
	store := NewFileOrderStore(centerDir)
	store.mu.Lock()
	defer store.mu.Unlock()
	info, err := os.Lstat(store.path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("catalog order must be a regular file")
	}
	file, err := store.readLocked()
	if err != nil {
		return false, err
	}
	target := "user:" + subject
	if _, exists := file.Users[target]; exists {
		return false, nil
	}
	legacy, exists := file.Users["user:app"]
	if !exists {
		return false, nil
	}
	if _, err = os.Lstat(backupPath); os.IsNotExist(err) {
		bytes, err := os.ReadFile(store.path)
		if err != nil {
			return false, err
		}
		if err = os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
			return false, err
		}
		backup, err := os.CreateTemp(filepath.Dir(backupPath), ".pins-backup-*")
		if err != nil {
			return false, err
		}
		temporary := backup.Name()
		defer os.Remove(temporary)
		_, writeErr := backup.Write(bytes)
		syncErr := backup.Sync()
		closeErr := backup.Close()
		if writeErr != nil {
			return false, writeErr
		}
		if syncErr != nil {
			return false, syncErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		if err := os.Rename(temporary, backupPath); err != nil {
			return false, err
		}

	} else if err != nil {
		return false, err
	}
	file.Users[target] = cloneOrder(legacy)
	return true, store.writeLocked(file)
}
