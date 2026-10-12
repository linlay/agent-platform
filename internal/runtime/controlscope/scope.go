// Package controlscope stores immutable, server-issued Run control identities.
// It is independent of HTTP, WebSocket sessions and mutable UI targets.
package controlscope

import (
	"context"
	"errors"
	"fmt"
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
	// In-process creation has no network transport; origin belongs to RunOrigin.
	return Scope{}
}

// Live is the process-local registry of Run control identities. Bind returns
// the owner after the call: an existing owner is never replaced.
type Live interface {
	BindRunControlScope(runID string, scope Scope) Scope
	RunControlScope(runID string) (Scope, bool)
}

// Store resolves a Run's control identity from process memory. Identities are
// never written to disk: a restart cancels every unfinished Run, and a new
// query establishes a new identity.
type Store struct{ Live Live }

// NewStore accepts the shared run manager.
func NewStore(runs any) Store {
	live, _ := runs.(Live)
	return Store{Live: live}
}

var ErrMissing = errors.New("run control identity unavailable")
var ErrConflict = errors.New("run control identity conflicts with existing run")

func (s Store) Load(runID string) (Scope, error) {
	if s.Live != nil {
		if scope, ok := s.Live.RunControlScope(runID); ok {
			return scope, nil
		}
	}
	return Scope{}, ErrMissing
}

// Bind claims a Run ID for one identity; a Run that already has an owner can
// only be bound again to that same owner.
func (s Store) Bind(runID string, scope Scope) error {
	if runID == "" {
		return fmt.Errorf("runId is required")
	}
	if s.Live == nil {
		return fmt.Errorf("run control registry is not configured")
	}
	if s.Live.BindRunControlScope(runID, scope) != scope {
		return ErrConflict
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
