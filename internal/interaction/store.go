package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store keeps immutable Run policies outside the public conversation protocol.
type Store struct{ Root string }

var ErrMissing = errors.New("run interaction policy unavailable")
var ErrConflict = errors.New("run interaction policy conflicts with existing run")

func (s Store) path(runID string) string {
	hash := sha256.Sum256([]byte(runID))
	return filepath.Join(s.Root, hex.EncodeToString(hash[:])+".json")
}
func (s Store) Load(runID string) (Config, error) {
	var config Config
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return config, ErrMissing
	}
	if err != nil {
		return config, err
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return config, err
	}
	return config, nil
}

// Bind publishes a complete file atomically without replacing an existing policy.
// Keeping records after completion prevents caller-supplied Run ID reuse from
// changing the policy, and permits waiting Runs to recover after a restart.
func (s Store) Bind(runID string, config Config) error {
	if runID == "" {
		return fmt.Errorf("runId is required")
	}
	if old, err := s.Load(runID); err == nil {
		if old != config {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, ErrMissing) {
		return err
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Root, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), s.path(runID)); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		old, readErr := s.Load(runID)
		if readErr != nil {
			return readErr
		}
		if old != config {
			return ErrConflict
		}
	}
	return nil
}
