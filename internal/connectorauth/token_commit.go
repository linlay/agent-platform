package connectorauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/connector"
)

// commitTokenCredentials publishes one accepted candidate while the manager and
// credential locks are held. A failed metadata update must not leave a different
// account's credentials active with the previous binding or verification state.
func (m *Manager) commitTokenCredentials(pkg connector.Package, values map[string]string, status string) error {
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return err
	}
	path, err := connector.CredentialsPath(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	names := []string{"credentials.json", "connection.json", "auth-state.json", "verification.json", "pending-credentials.json"}
	previous := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		file := filepath.Join(dir, name)
		info, statErr := os.Lstat(file)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("connector credential state unavailable")
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil || !json.Valid(data) {
			return fmt.Errorf("connector credential state unavailable")
		}
		previous[name] = data
	}
	rollback := func() error {
		for _, name := range names {
			file := filepath.Join(dir, name)
			if data, exists := previous[name]; exists {
				if err := savePrivateJSON(file, data); err != nil {
					return fmt.Errorf("connector credential commit failed; prior state could not be fully restored")
				}
			} else if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("connector credential commit failed; prior state could not be fully restored")
			}
		}
		return fmt.Errorf("connector credential commit failed; saved credentials were preserved")
	}
	if err = savePrivateJSON(path, values); err != nil {
		return rollback()
	}
	if err = m.markConfigured(pkg); err != nil {
		return rollback()
	}
	if err = changeAuthState(pkg.PersistentRoot(), pkg.ID, false); err != nil {
		return rollback()
	}
	if err = saveVerification(pkg, values, status); err != nil {
		return rollback()
	}
	if err = os.Remove(filepath.Join(dir, "pending-credentials.json")); err != nil && !os.IsNotExist(err) {
		return rollback()
	}
	return nil
}
