package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RetireSharedRuntime is called only after Agent runtimes have been rebuilt.
// Keep the obsolete tree in a migration backup rather than discarding files
// that may have been added manually to a former generated directory.
func (s Sources) RetireSharedRuntime(runtimeHome string) error {
	if runtimeHome == "" {
		return nil
	}
	old := filepath.Join(runtimeHome, "ru-connectors")
	info, err := os.Lstat(old)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("legacy ru-connectors must be a real directory")
	}
	for _, root := range []string{s.ExternalRoot, s.BuiltinRoot, s.PersistentRoot()} {
		if RootsOverlap(old, root) {
			return fmt.Errorf("legacy ru-connectors overlaps current connector sources or state")
		}
	}
	backup := filepath.Join(runtimeHome, ".connector-layout-backup-"+time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.Mkdir(backup, 0700); err != nil {
		return err
	}
	if err := os.Rename(old, filepath.Join(backup, "ru-connectors")); err != nil {
		_ = os.Remove(backup)
		return err
	}
	return nil
}
