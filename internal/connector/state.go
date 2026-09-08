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
	if !ValidID(id) || IsBuiltin(id) || strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("invalid external connector state root or id")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for _, p := range []string{root, filepath.Join(root, id)} {
		if info, err := os.Lstat(p); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
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

// InternalStateLink validates an npm-style relative link inside one connector's
// state. Migrations preserve its text without following it while copying files.
func InternalStateLink(owner, path string) (string, error) {
	link, err := os.Readlink(path)
	if err != nil || filepath.IsAbs(link) {
		return "", fmt.Errorf("state link must be relative: %s", path)
	}
	info, err := os.Lstat(owner)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("state owner must be a real directory: %s", owner)
	}
	canonicalOwner, err := filepath.EvalSymlinks(owner)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(canonicalOwner, target)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("state link escapes its connector: %s", path)
	}
	// The lexical target must stay inside too, so relocation cannot change a
	// link that originally reached its owner through an external alias.
	inside, err = filepath.Rel(owner, filepath.Join(filepath.Dir(path), link))
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("state link escapes its connector: %s", path)
	}
	return link, nil
}
