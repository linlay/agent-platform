package kbase

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type OrphanStorage struct {
	Path          string `json:"path"`
	SizeBytes     int64  `json:"sizeBytes"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	PossibleOwner string `json:"possibleOwner,omitempty"`
}

// AuditOrphanStorage is deliberately read-only. It reports runtime KBASE
// directories no current agent owns and never removes them.
func (m *Manager) AuditOrphanStorage() ([]OrphanStorage, error) {
	if m == nil || strings.TrimSpace(m.options.RuntimeDir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(m.options.RuntimeDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owned := map[string]struct{}{}
	if m.agents != nil {
		for _, spec := range m.agents.Agents() {
			if !spec.Enabled {
				continue
			}
			location := strings.ToLower(strings.TrimSpace(spec.Config.Storage.Location))
			if location == "" || location == "runtime" {
				owned[storageLockKey(filepath.Join(m.options.RuntimeDir, spec.Key))] = struct{}{}
			}
		}
	}
	var orphans []OrphanStorage
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := storageLockKey(filepath.Join(m.options.RuntimeDir, entry.Name()))
		if _, ok := owned[root]; ok {
			continue
		}
		orphan := OrphanStorage{Path: root}
		_ = filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			info, infoErr := item.Info()
			if infoErr != nil {
				return nil
			}
			if !item.IsDir() {
				orphan.SizeBytes += info.Size()
			}
			if modified := info.ModTime().UnixMilli(); modified > orphan.LastUsedAt {
				orphan.LastUsedAt = modified
			}
			return nil
		})
		orphans = append(orphans, orphan)
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Path < orphans[j].Path })
	return orphans, nil
}
