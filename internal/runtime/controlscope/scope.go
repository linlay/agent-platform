// Package controlscope stores immutable, server-issued Run control identities.
// It is independent of HTTP, WebSocket sessions and mutable UI targets.
package controlscope

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Scope struct {
	Transport string `json:"transport"`
	Lane      string `json:"lane"`
	Subject   string `json:"subject,omitempty"`
	Boundary  string `json:"boundary,omitempty"`
}

type contextKey struct{}

func WithContext(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, contextKey{}, scope)
}
func FromContext(ctx context.Context) Scope {
	if s, ok := ctx.Value(contextKey{}).(Scope); ok {
		return s
	}
	return Scope{Transport: "internal"}
}

type Store struct{ Root string }

var ErrMissing = errors.New("run control identity unavailable")
var ErrConflict = errors.New("run control identity conflicts with existing run")

func (s Store) path(runID string) string {
	hash := sha256.Sum256([]byte(runID))
	return filepath.Join(s.Root, hex.EncodeToString(hash[:])+".json")
}
func (s Store) Load(runID string) (Scope, error) {
	var scope Scope
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return scope, ErrMissing
	}
	if err != nil {
		return scope, err
	}
	if err = json.Unmarshal(data, &scope); err != nil {
		return scope, err
	}
	if scope.Transport != "http" && scope.Transport != "ws" && scope.Transport != "internal" {
		return scope, fmt.Errorf("invalid stored control transport")
	}
	return scope, nil
}

// Bind publishes a complete file atomically without replacing an existing owner.
// Keeping records after completion prevents caller-supplied Run ID reuse from
// changing ownership, and permits waiting Runs to recover after a restart.
func (s Store) Bind(runID string, scope Scope) error {
	if runID == "" {
		return fmt.Errorf("runId is required")
	}
	if old, err := s.Load(runID); err == nil {
		if old != scope {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, ErrMissing) {
		return err
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Root, ".scope-*")
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
		if old != scope {
			return ErrConflict
		}
	}
	return nil
}

func Check(owner, caller Scope) string {
	if owner.Transport != caller.Transport {
		return "run_transport_mismatch"
	}
	if owner.Subject != caller.Subject || owner.Boundary != caller.Boundary {
		return "run_control_identity_mismatch"
	}
	if owner.Transport == "ws" && owner.Lane != caller.Lane {
		return "run_lane_mismatch"
	}
	return ""
}
