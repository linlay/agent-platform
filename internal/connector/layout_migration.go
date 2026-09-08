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
	if legacy != center && (RootsOverlap(legacy, center) || RootsOverlap(legacy, s.StateRoot) || RootsOverlap(legacy, s.RuntimeRoot) || RootsOverlap(legacy, s.BuiltinRoot)) {
		return fmt.Errorf("legacy connector directory overlaps a current connector root")
	}
	type move struct{ source, target string }
	var moves []move
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
		if info.Mode()&os.ModeSymlink != 0 && !safeLegacyStateLink(source, legacy, center) {
			return fmt.Errorf("legacy connector layout contains an unsafe link: %s", source)
		}
		targetInfo, targetErr := os.Lstat(target)
		if merge && info.IsDir() {
			if targetErr == nil && (!targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("connector state destination is not a directory: %s", target)
			}
			if targetErr != nil && !os.IsNotExist(targetErr) {
				return targetErr
			}
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
	roots := []string{center}
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
				target = filepath.Join(s.StateRoot, entry.Name())
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
	if err := os.MkdirAll(center, 0700); err != nil {
		return err
	}
	var applied []move
	rollback := func(cause error) error {
		for i := len(applied) - 1; i >= 0; i-- {
			if err := os.Rename(applied[i].target, applied[i].source); err != nil {
				return fmt.Errorf("%w; restoring %s: %v", cause, applied[i].source, err)
			}
		}
		return cause
	}
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.target), 0o700); err != nil {
			return rollback(err)
		}
		if err := os.Rename(m.source, m.target); err != nil {
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
	link, err := os.Readlink(source)
	if err != nil || filepath.IsAbs(link) {
		return false
	}
	target, err := filepath.EvalSymlinks(source)
	if err != nil {
		return false
	}
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
		owner, err := filepath.EvalSymlinks(filepath.Join(state, parts[0]))
		if err != nil {
			continue
		}
		inside, err := filepath.Rel(owner, target)
		if err == nil && inside != ".." && !strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
