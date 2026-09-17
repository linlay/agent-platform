package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"agent-platform/internal/connector"
)

// Connector-bearing Agents publish a complete candidate, including package
// symlinks. Normal Agents keep the existing stable-root synchronization.
func publishAgentCandidate(candidate, stable string, hasConnectors bool) error {
	return publishAgentCandidateWithRename(candidate, stable, hasConnectors, func(stage, source, target string) error {
		return renameRuntimeAgent(stage, source, target, runtime.GOOS, os.Rename, time.Sleep)
	})
}
func publishAgentCandidateWithRename(candidate, stable string, hasConnectors bool, rename func(string, string, string) error) error {
	if !hasConnectors {
		entries, _ := os.ReadDir(filepath.Join(stable, "connectors"))
		hasConnectors = len(entries) > 0
	}
	if !hasConnectors {
		return syncRuntimeTree(candidate, stable)
	}
	want, err := connector.RuntimeFingerprint(candidate)
	if err != nil {
		return err
	}
	if got, err := connector.RuntimeFingerprint(stable); err == nil && want == got {
		return nil
	}
	backup := candidate + ".previous"
	hadOld := false
	if _, err := os.Lstat(stable); err == nil {
		if err := rename("backup", stable, backup); err != nil {
			return err
		}
		hadOld = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := rename("publish", candidate, stable); err != nil {
		if hadOld {
			if restoreErr := rename("rollback", backup, stable); restoreErr != nil {
				return fmt.Errorf("%w; rollback=failed; previous_agent=%q; rollback_error=%v", err, backup, restoreErr)
			}
			return fmt.Errorf("%w; rollback=restored; stable=%q", err, stable)
		}
		return fmt.Errorf("%w; rollback=not_needed (no previous Agent)", err)
	}
	if hadOld {
		// An old Windows process may still hold its executable. Retain the
		// hidden backup until startup cleanup instead of undoing publication.
		_ = os.RemoveAll(backup)
	}
	return nil
}
