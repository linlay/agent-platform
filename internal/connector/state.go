package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StateDir resolves one connector's private directory below the connector
// namespace (<state-dir>/connectors). It never creates or reads credentials.
func StateDir(root, id string) (string, error) {
	if !ValidID(id) || strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("invalid connector state root or id")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for _, p := range []string{root, filepath.Join(root, id)} {
		if info, err := os.Lstat(p); err == nil {
			if !info.IsDir() || unsafeDirectoryAlias(p, info) {
				return "", fmt.Errorf("connector state must be a real directory: %s", p)
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return filepath.Join(root, id), nil
}

// CredentialsPath is the deployment token-template store for one connector.
// OAuth credentials are maintained separately in oauth.json in the same dir.
func CredentialsPath(root, id string) (string, error) {
	dir, err := StateDir(root, id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "credentials.json")
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("invalid connector credential file")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return p, nil
}
