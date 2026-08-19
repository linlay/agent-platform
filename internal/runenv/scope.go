package runenv

import (
	"fmt"
	"sync"
)

// Scope is the lightweight run-owned handle for a dynamic environment. It
// materializes a State and checkpoint only after the first successful set.
type Scope struct {
	mu       sync.RWMutex
	identity Identity
	store    *Store
	state    *State
	closed   bool
}

func newScope(identity Identity, store *Store) *Scope {
	return &Scope{identity: identity, store: store}
}

func (s *Scope) Revision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	state := s.state
	closed := s.closed
	s.mu.RUnlock()
	if closed || state == nil {
		return 0
	}
	return state.Revision()
}

func (s *Scope) Snapshot() (map[string]string, uint64, error) {
	if s == nil {
		return map[string]string{}, 0, nil
	}
	s.mu.RLock()
	state := s.state
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return nil, 0, ErrClosed
	}
	if state == nil {
		return map[string]string{}, 0, nil
	}
	return state.Snapshot()
}

func (s *Scope) Mutate(request MutationRequest) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, fmt.Errorf("run environment is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return MutationResult{}, ErrClosed
	}
	if s.state == nil {
		if request.ExpectedRevision != nil && *request.ExpectedRevision != 0 {
			return MutationResult{}, fmt.Errorf("%w: expected %d, current 0", ErrRevisionConflict, *request.ExpectedRevision)
		}
		if request.Operation == OperationUnset {
			name := NormalizeName(request.Name)
			if err := ValidateName(name, s.store.limits.ExtraDeniedKeys); err != nil {
				return MutationResult{}, err
			}
			return MutationResult{}, fmt.Errorf("%w: %s", ErrKeyNotSet, name)
		}
		if request.Operation != OperationSet {
			return MutationResult{}, fmt.Errorf("unsupported run environment mutation %q", request.Operation)
		}
		name := NormalizeName(request.Name)
		if err := ValidateName(name, s.store.limits.ExtraDeniedKeys); err != nil {
			return MutationResult{}, err
		}
		if err := ValidateValue(request.Value, s.store.limits.MaxValueBytes); err != nil {
			return MutationResult{}, fmt.Errorf("%s: %w", name, err)
		}
		if len([]byte(request.Value)) > s.store.limits.MaxTotalBytes {
			return MutationResult{}, fmt.Errorf("run environment exceeds %d total bytes", s.store.limits.MaxTotalBytes)
		}
		state, err := s.store.materialize(s.identity)
		if err != nil {
			return MutationResult{}, err
		}
		result, err := state.Mutate(request)
		if err != nil {
			_ = state.Destroy()
			return MutationResult{}, err
		}
		s.state = state
		return result, nil
	}
	return s.state.Mutate(request)
}

func (s *Scope) Destroy() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	state := s.state
	s.state = nil
	s.mu.Unlock()
	if state == nil {
		return nil
	}
	return state.Destroy()
}
