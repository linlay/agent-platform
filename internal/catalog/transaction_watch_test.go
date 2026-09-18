package catalog

import "testing"

func TestTransactionWatchFilteringIncludesAncestors(t *testing.T) {
	for _, name := range []string{".skill-package-import-123", ".skill-backup-123", ".skill-package-backup-123", ".skill-package-record-123", ".package", ".connector-delete-123", "demo.backup-123"} {
		if ShouldWatchRuntimeDir(name) {
			t.Errorf("watch registered for %s", name)
		}
		for _, path := range []string{"/runtime/skills-center/" + name + "/nested/SKILL.md", `D:\runtime\skills-center\` + name + `\nested\SKILL.md`} {
			if !ShouldIgnoreRuntimeWatchPath(path) {
				t.Errorf("transaction event not ignored: %s", path)
			}
		}
	}
	for _, path := range []string{"/runtime/skills-center/demo/SKILL.md", "/runtime/skills-center/demo/.config/settings.json"} {
		if ShouldIgnoreRuntimeWatchPath(path) {
			t.Errorf("live skill event ignored: %s", path)
		}
	}
}
