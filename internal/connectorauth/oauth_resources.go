package connectorauth

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func resourceCredentialPath(root, id, resource, destination string) (string, error) {
	dir, err := StateDir(root, id)
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "oauth-resources")
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("invalid resource credential directory")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if destination == "" {
		destination = resource
	}
	sum := sha256.Sum256([]byte(resource + "\x00" + destination))
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sum))
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("invalid resource credential file")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func saveResourceCredential(root, id string, c oauthCredential) error {
	return saveCredential(root, id, c)
}

func readResourceCredential(root, id, resource string, destinations []string) (oauthCredential, string, error) {
	c, err := readCredential(root, id)
	if err != nil {
		return oauthCredential{}, "", err
	}
	c, ok := selectCredential(c, resource, destinations)
	if !ok {
		return oauthCredential{}, "", fmt.Errorf("connector requires login")
	}
	path, err := credentialPath(root, id)
	return c, path, err
}

// Called while the connector credential lock is held, after canceling logins.
func deleteOAuthCredentials(root, id string) error {
	legacy, err := credentialPath(root, id)
	if err != nil {
		return err
	}
	dir, err := StateDir(root, id)
	if err != nil {
		return err
	}
	resources := filepath.Join(dir, "oauth-resources")
	if info, err := os.Lstat(resources); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("invalid resource credential directory")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(resources)
}
