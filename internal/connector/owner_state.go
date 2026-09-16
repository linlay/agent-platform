package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OwnerStateRoot uses a trusted server subject and never accepts a path from a client.
// Legacy ownerless callers remain explicit deployment management; HTTP never uses it.
func OwnerStateRoot(root, owner string) string {
	if owner == "" {
		return root
	}
	sum := sha256.Sum256([]byte(owner))
	return filepath.Join(root, "users", hex.EncodeToString(sum[:]))
}
func (p Package) CredentialRoot() string        { return OwnerStateRoot(p.PersistentRoot(), p.Owner) }
func (p Package) UserStateDir() (string, error) { return StateDir(p.CredentialRoot(), p.ID) }
func (p Package) InstallDir() (string, error) {
	if !ValidID(p.ID) || !validVersion(p.Version) || strings.TrimSpace(p.PersistentRoot()) == "" {
		return "", fmt.Errorf("invalid connector installation")
	}
	dir := filepath.Join(p.PersistentRoot(), "installations", p.ID, p.Version)
	for current := dir; current != filepath.Dir(p.PersistentRoot()); current = filepath.Dir(current) {
		if info, err := os.Lstat(current); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", fmt.Errorf("invalid connector installation directory")
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return dir, nil
}
