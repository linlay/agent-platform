package catalog

import (
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/connector"
)

// Connector-bearing Agents publish a complete candidate, including package
// symlinks. Normal Agents keep the existing stable-root synchronization.
func publishAgentCandidate(candidate, stable string, hasConnectors bool) error {
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
		if err := os.Rename(stable, backup); err != nil {
			return err
		}
		hadOld = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(candidate, stable); err != nil {
		if hadOld {
			if restoreErr := os.Rename(backup, stable); restoreErr != nil {
				return fmt.Errorf("%w; restore failed, previous Agent retained at %s: %v", err, backup, restoreErr)
			}
		}
		return err
	}
	if hadOld {
		// An old Windows process may still hold its executable. Retain the
		// hidden backup until startup cleanup instead of undoing publication.
		_ = os.RemoveAll(backup)
	}
	return nil
}
