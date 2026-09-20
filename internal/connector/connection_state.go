package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConnectionState is this instance's configuration completion, independent of expiring credentials.
type ConnectionState struct {
	ConnectorID string `json:"connectorId"`
	Configured  bool   `json:"configured"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

var connectionStateMu sync.Mutex

func (p Package) ReadConnection() (ConnectionState, error) {
	state := ConnectionState{ConnectorID: p.ID}
	dir, err := p.ConnectorStateDir()
	if err != nil {
		return state, err
	}
	err = ReadJSON(filepath.Join(dir, "connection.json"), &state)
	if os.IsNotExist(err) {
		return ConnectionState{ConnectorID: p.ID}, nil
	}
	if err != nil {
		return state, fmt.Errorf("connection state unavailable")
	}
	if state.ConnectorID != p.ID {
		return state, fmt.Errorf("invalid connection state")
	}
	return state, nil
}
func (p Package) SetConfigured(configured bool) (ConnectionState, error) {
	connectionStateMu.Lock()
	defer connectionStateMu.Unlock()
	s, err := p.ReadConnection()
	if err != nil {
		return s, err
	}
	s.Configured = configured
	s.UpdatedAt = time.Now().UnixMilli()
	dir, err := p.ConnectorStateDir()
	if err != nil {
		return s, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return s, err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	f, err := os.CreateTemp(dir, ".connection-*")
	if err != nil {
		return s, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return s, err
	}
	if closeErr != nil {
		return s, closeErr
	}
	return s, os.Rename(name, filepath.Join(dir, "connection.json"))
}

// ConnectorStateDir is shared by all requests to this Platform instance.
func (p Package) ConnectorStateDir() (string, error) { return StateDir(p.PersistentRoot(), p.ID) }
func (p Package) CredentialRoot() string             { return p.PersistentRoot() }

// RequireConfigured resolves configuration at dispatch time, including for frozen Runs.
func RequireConfigured(root, id string) error {
	if id == "" {
		return nil
	}
	p := Package{Manifest: Manifest{ID: id}, StateRoot: root}
	state, err := p.ReadConnection()
	if err != nil {
		return err
	}
	if !state.Configured {
		return fmt.Errorf("connector_configuration_required")
	}
	return nil
}
