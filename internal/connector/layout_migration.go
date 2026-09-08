package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MigrateLegacy moves installed sources and persistent state into their new
// roots without reading the retired MCP registry. It is idempotent and never
// overwrites a destination. Old builtin copies are retained in a sibling migration backup.
func (s Sources) MigrateLegacy(legacy string) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	return s.migrateLegacy(legacy, os.Rename)
}

func (s Sources) migrateLegacy(legacy string, rename func(string, string) error) error {
	if s.ExternalRoot == "" || s.StateRoot == "" {
		return nil
	}
	if err := s.ValidateRoots(); err != nil {
		return err
	}
	if legacy == "" {
		legacy = filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	}
	legacy, err := filepath.Abs(legacy)
	if err != nil {
		return err
	}
	center, err := filepath.Abs(s.ExternalRoot)
	if err != nil {
		return err
	}
	if legacy != center && (RootsOverlap(legacy, center) || RootsOverlap(legacy, s.StateRoot) || RootsOverlap(legacy, s.BuiltinRoot)) {
		return fmt.Errorf("legacy connector directory overlaps a current connector root")
	}
	oldState := s.LegacyStateRoot
	if oldState == "" {
		oldState = filepath.Join(filepath.Dir(center), "connector-state")
	}
	oldState, err = filepath.Abs(oldState)
	if err != nil {
		return err
	}
	for _, current := range []string{legacy, center, s.StateRoot, s.BuiltinRoot} {
		if RootsOverlap(oldState, current) {
			return fmt.Errorf("legacy connector state overlaps another connector root: %s", oldState)
		}
	}
	type move struct{ source, target string }
	var moves []move
	var directories []string
	planned := map[string]bool{}
	var plan func(string, string, bool) error
	plan = func(source, target string, merge bool) error {
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 && !safeLegacyStateLink(source, legacy, center, oldState) {
			return fmt.Errorf("legacy connector layout contains an unsafe link: %s", source)
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported connector migration file: %s", source)
		}
		targetInfo, targetErr := os.Lstat(target)
		if merge && info.IsDir() {
			if targetErr == nil && (!targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("connector state destination is not a directory: %s", target)
			}
			if targetErr != nil && !os.IsNotExist(targetErr) {
				return targetErr
			}
			if planned[target] {
				return fmt.Errorf("connector migration destination already planned as a file: %s", target)
			}
			directories = append(directories, target)
			entries, err := os.ReadDir(source)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := plan(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), true); err != nil {
					return err
				}
			}
			return nil
		}
		if targetErr == nil || planned[target] {
			return fmt.Errorf("connector migration destination already exists: %s", target)
		}
		if !os.IsNotExist(targetErr) {
			return targetErr
		}

		planned[target] = true
		moves = append(moves, move{source, target})
		return nil
	}
	backupRoot := filepath.Join(filepath.Dir(center), ".connector-layout-backup-"+time.Now().UTC().Format("20060102T150405.000000000"))
	roots := []string{center, oldState}
	if legacy != center {
		roots = append([]string{legacy}, roots...)
	}
	for _, root := range roots {
		if info, err := os.Lstat(root); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("legacy connector root must be a directory: %s", root)
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			target := filepath.Join(center, entry.Name())
			state := entry.Name() == ".state" || entry.Name() == ".credentials"
			if state {
				source := filepath.Join(root, entry.Name())
				info, err := os.Lstat(source)
				if err != nil {
					return err
				}
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("legacy connector state must be a real directory: %s", source)
				}
				items, err := os.ReadDir(source)
				if err != nil {
					return err
				}
				for _, item := range items {
					id := item.Name()
					if entry.Name() == ".credentials" {
						if !strings.HasSuffix(id, ".json") || !item.Type().IsRegular() {
							return fmt.Errorf("invalid legacy credential file: %s", filepath.Join(source, id))
						}
						id = strings.TrimSuffix(id, ".json")
					} else if !item.IsDir() {
						return fmt.Errorf("legacy connector state must be a real directory: %s", filepath.Join(source, id))
					}
					target, err := StateDir(s.StateRoot, id)
					if err != nil {
						return err
					}
					if entry.Name() == ".credentials" {
						directories = append(directories, target)
						target = filepath.Join(target, "credentials.json")
					}
					if err := plan(filepath.Join(source, item.Name()), target, entry.Name() == ".state"); err != nil {
						return err
					}
				}
				continue
			} else if root == oldState {
				// Only the known state layouts belong to this migration.
				continue
			} else if IsBuiltin(entry.Name()) || entry.Name() == ".builtin-state" {
				scope := "connectors"
				if root == center {
					scope = "connectors-center"
				}
				target = filepath.Join(backupRoot, scope, entry.Name())
			} else if root == center {
				continue
			}
			if err := plan(filepath.Join(root, entry.Name()), target, state); err != nil {
				return err
			}
		}
	}
	for _, dir := range directories {
		if planned[dir] {
			return fmt.Errorf("connector migration destination is both file and directory: %s", dir)
		}
	}
	var created []string
	var mkdir func(string) error
	mkdir = func(dir string) error {
		if info, err := os.Lstat(dir); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("migration destination must be a real directory: %s", dir)
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := mkdir(filepath.Dir(dir)); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		created = append(created, dir)
		return nil
	}
	var applied []move
	rollback := func(cause error) error {
		for i := len(applied) - 1; i >= 0; i-- {
			if err := rename(applied[i].target, applied[i].source); err != nil {
				return fmt.Errorf("%w; restoring %s: %v", cause, applied[i].source, err)
			}
		}
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i])
		}
		return cause
	}
	for _, dir := range append([]string{center}, directories...) {
		if err := mkdir(dir); err != nil {
			return rollback(err)
		}
	}
	for _, m := range moves {
		if err := mkdir(filepath.Dir(m.target)); err != nil {
			return rollback(err)
		}
		// Recheck immediately before mutation; a conflicting destination is
		// never intentionally replaced, including one created after preflight.
		if _, err := os.Lstat(m.target); !os.IsNotExist(err) {
			return rollback(fmt.Errorf("connector migration destination appeared: %s", m.target))
		}
		if err := rename(m.source, m.target); err != nil {
			return rollback(err)
		}
		applied = append(applied, m)
	}
	for _, root := range roots {
		removeEmptyMigrationDirs(filepath.Join(root, ".state"))
		removeEmptyMigrationDirs(filepath.Join(root, ".credentials"))
	}
	if legacy != center {
		_ = os.Remove(legacy)
	} // remove only an empty legacy root
	_ = os.Remove(oldState)
	return nil
}

func removeEmptyMigrationDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			removeEmptyMigrationDirs(filepath.Join(root, entry.Name()))
		}
	}
	_ = os.Remove(root) // rmdir succeeds only if empty; files are never removed
}

// npm creates relative executable links inside a connector's state directory.
// Moving those leaf links preserves their meaning without following them or
// granting access to another connector's state. Linked state roots are rejected.
func safeLegacyStateLink(source string, roots ...string) bool {
	for _, root := range roots {
		state := filepath.Join(root, ".state")
		rel, err := filepath.Rel(state, source)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 || !ValidID(parts[0]) {
			continue
		}
		if _, err := InternalStateLink(filepath.Join(state, parts[0]), source); err == nil {
			return true
		}
	}
	return false
}
