// Package operationstate persists bounded, credential-free operation outcomes.
package operationstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

func Path(root, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(root, hex.EncodeToString(sum[:])+".json")
}
func Write(root, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), Path(root, id))
}
func Read(root, id string, value any) error {
	data, err := os.ReadFile(Path(root, id))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
