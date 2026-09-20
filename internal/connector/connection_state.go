package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConnectionState is this instance's preference, independent of expiring credentials.
type ConnectionState struct {
	ConnectorID string `json:"connectorId"`
	Bound       bool   `json:"bound"`
	Enabled     bool   `json:"enabled"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

var connectionStateMu sync.Mutex

func (p Package) ReadConnection() (ConnectionState, error) {
	state := ConnectionState{ConnectorID: p.ID}
	if p.Builtin {
		state.Bound = true
		state.Enabled = true
		return state, nil
	}
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
	if state.ConnectorID != p.ID || state.Enabled && !state.Bound {
		return state, fmt.Errorf("invalid connection state")
	}
	return state, nil
}
func (p Package) UpdateConnection(bound *bool, enabled *bool) (ConnectionState, error) {
	connectionStateMu.Lock()
	defer connectionStateMu.Unlock()
	s, err := p.ReadConnection()
	if err != nil {
		return s, err
	}
	if p.Builtin {
		return s, ErrBuiltinReadOnly
	}
	if bound != nil {
		s.Bound = *bound
		if !s.Bound {
			s.Enabled = false
		}
	}
	if enabled != nil {
		s.Enabled = *enabled
	}
	if s.Enabled && !s.Bound {
		return s, fmt.Errorf("connection_required")
	}
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

// RequireEnabled resolves preferences at dispatch time, including for frozen Runs.
func RequireEnabled(root, id string) error {
	if id == "" || IsBuiltin(id) {
		return nil
	}
	p := Package{Manifest: Manifest{ID: id}, StateRoot: root}
	state, err := p.ReadConnection()
	if err != nil {
		return err
	}
	if !state.Bound || !state.Enabled {
		return fmt.Errorf("connector_disabled")
	}
	return nil
}
