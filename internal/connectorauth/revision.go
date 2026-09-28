package connectorauth

import (
	"crypto/rand"
	"os"
	"path/filepath"
)

// authState is a durable logout fence as well as a cross-process notification.
// Generation changes on logout; Revision changes on every successful mutation.
type authState struct {
	Generation string `json:"generation"`
	Revision   string `json:"revision"`
}

func readAuthState(root, id string) (authState, error) {
	dir, err := StateDir(root, id)
	if err != nil {
		return authState{}, err
	}
	var s authState
	err = readPrivateJSON(filepath.Join(dir, "auth-state.json"), &s)
	if os.IsNotExist(err) {
		return s, nil
	}
	return s, err
}

// Caller holds the credential lock.
func changeAuthState(root, id string, logout bool) error {
	s, err := readAuthState(root, id)
	if err != nil {
		return err
	}
	if logout {
		s.Generation = rand.Text()
	}
	s.Revision = rand.Text()
	dir, err := StateDir(root, id)
	if err != nil {
		return err
	}
	return savePrivateJSON(filepath.Join(dir, "auth-state.json"), s)
}
