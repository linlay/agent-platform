package reload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
)

func pathWithin(root, path string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Hash bytes, paths and modes, not timestamps: equal-size edits and restored
// mtimes must not hide a change. Never follow symlinks or open special files.
func catalogFingerprint(ctx context.Context, entry watchEntry, centers []string) (string, error) {
	h := sha256.New()
	digest := sha256.New()
	buffer := make([]byte, 32*1024)
	err := filepath.WalkDir(entry.path, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == entry.path && os.IsNotExist(walkErr) {
				_, _ = io.WriteString(h, "missing")
				return nil
			}
			return walkErr
		}
		if path != entry.path && (shouldIgnoreBackgroundWatchPath(path, centers...) || (d.IsDir() && !catalog.ShouldWatchRuntimeDir(d.Name()))) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(entry.path, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%q:%d:", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "%q\n", target)
			return nil
		}
		if !info.Mode().IsRegular() {
			_, _ = io.WriteString(h, "\n")
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return fmt.Errorf("catalog source changed during scan: %s", path)
		}
		digest.Reset()
		// Hide File.WriteTo so CopyBuffer reuses one buffer across all files.
		n, err := io.CopyBuffer(digest, struct{ io.Reader }{f}, buffer)
		if err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("catalog source changed during scan: %s", path)
		}
		_, _ = fmt.Fprintf(h, "%d:%x\n", n, digest.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Only acknowledge the requested category. A skills cascade may read agents,
// but must not consume an unrelated pending model/provider/connector change.
func (r *RuntimeCatalogReloader) watchEntries(reason string) []watchEntry {
	if r.background == nil {
		return nil
	}
	for _, entry := range r.background.entries {
		if entry.reason == reason {
			return []watchEntry{entry}
		}
	}
	switch reason {
	case "agents", "teams", "skills", "models", "providers", "tools", "connectors", "viewport-servers", "viewports":
		return nil
	default:
		return r.background.entries
	}
}

func (r *RuntimeCatalogReloader) captureWatchState(ctx context.Context, reason string) map[string]string {
	states := make(map[string]string)
	for _, entry := range r.watchEntries(reason) {
		if state, err := catalogFingerprint(ctx, entry, r.background.centers); err == nil {
			states[entry.reason] = state
		}
	}
	return states
}

func (r *RuntimeCatalogReloader) invalidateWatchState(reason string) {
	for _, entry := range r.watchEntries(reason) {
		delete(r.background.loaded, entry.reason)
	}
}

func (r *RuntimeCatalogReloader) acknowledgeWatchState(ctx context.Context, before map[string]string) {
	if r.background == nil {
		return
	}
	for _, entry := range r.background.entries {
		state, ok := before[entry.reason]
		if !ok {
			continue
		}
		after, err := catalogFingerprint(ctx, entry, r.background.centers)
		if err == nil && after == state {
			r.background.loaded[entry.reason] = state
		} else {
			r.background.enqueue(entry.reason)
		}
	}
}

func (r *RuntimeCatalogReloader) reconcileWatch(ctx context.Context, reason string) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range r.watchEntries(reason) {
		state, err := catalogFingerprint(ctx, entry, r.background.centers)
		if err != nil {
			return err
		}
		if loaded, ok := r.background.loaded[reason]; ok && loaded == state {
			return nil
		}
	}
	return r.reloadLocked(ctx, reason)
}
