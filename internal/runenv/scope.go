package runenv

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrClosed           = errors.New("run environment is closed")
	ErrRevisionConflict = errors.New("run environment revision conflict")
	ErrKeyNotSet        = errors.New("run environment key was not set by the current run")
)

type Limits struct {
	MaxDynamicKeys  int
	MaxValueBytes   int
	MaxTotalBytes   int
	ExtraDeniedKeys []string
}

type Operation string

const (
	OperationSet   Operation = "set"
	OperationUnset Operation = "unset"
)

type MutationRequest struct {
	Operation             Operation
	Name                  string
	Value                 string
	ExpectedRevision      *uint64
	IdempotencyKey        string
	DefaultIdempotencyKey string
}

type MutationResult struct {
	Key        string `json:"key"`
	Revision   uint64 `json:"revision"`
	Changed    bool   `json:"changed"`
	Idempotent bool   `json:"idempotent"`
}

type storedIdempotency struct {
	Operation Operation
	Name      string
	Value     string
	Result    MutationResult
}

// Scope is the process-local dynamic environment owned by one root run.
// Its values are never persisted or restored after a Platform restart.
type Scope struct {
	mu          sync.RWMutex
	limits      Limits
	values      map[string]string
	revision    uint64
	closed      bool
	idempotency map[string]storedIdempotency
}

func NewScope(limits Limits) *Scope {
	return &Scope{
		limits:      normalizeLimits(limits),
		values:      map[string]string{},
		idempotency: map[string]storedIdempotency{},
	}
}

func (s *Scope) Revision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0
	}
	return s.revision
}

func (s *Scope) Snapshot() (map[string]string, uint64, error) {
	if s == nil {
		return map[string]string{}, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, 0, ErrClosed
	}
	out := make(map[string]string, len(s.values))
	for name, value := range s.values {
		out[name] = value
	}
	return out, s.revision, nil
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

	name := NormalizeName(request.Name)
	if err := ValidateName(name, s.limits.ExtraDeniedKeys); err != nil {
		return MutationResult{}, err
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = strings.TrimSpace(request.DefaultIdempotencyKey)
	}
	if idempotencyKey != "" {
		if previous, ok := s.idempotency[idempotencyKey]; ok {
			if previous.Operation != request.Operation || previous.Name != name || previous.Value != request.Value {
				return MutationResult{}, fmt.Errorf("idempotency key was already used with different arguments")
			}
			result := previous.Result
			result.Idempotent = true
			return result, nil
		}
	}
	if request.ExpectedRevision != nil && *request.ExpectedRevision != s.revision {
		return MutationResult{}, fmt.Errorf("%w: expected %d, current %d", ErrRevisionConflict, *request.ExpectedRevision, s.revision)
	}

	changed := false
	switch request.Operation {
	case OperationSet:
		if err := ValidateValue(request.Value, s.limits.MaxValueBytes); err != nil {
			return MutationResult{}, fmt.Errorf("%s: %w", name, err)
		}
		previous, exists := s.values[name]
		if !exists && len(s.values) >= s.limits.MaxDynamicKeys {
			return MutationResult{}, fmt.Errorf("run environment exceeds %d dynamic keys", s.limits.MaxDynamicKeys)
		}
		total := len(request.Value)
		for existingName, value := range s.values {
			if existingName != name {
				total += len(value)
			}
		}
		if total > s.limits.MaxTotalBytes {
			return MutationResult{}, fmt.Errorf("run environment exceeds %d total bytes", s.limits.MaxTotalBytes)
		}
		if !exists || previous != request.Value {
			s.values[name] = request.Value
			changed = true
		}
	case OperationUnset:
		if _, exists := s.values[name]; !exists {
			return MutationResult{}, fmt.Errorf("%w: %s", ErrKeyNotSet, name)
		}
		delete(s.values, name)
		changed = true
	default:
		return MutationResult{}, fmt.Errorf("unsupported run environment mutation %q", request.Operation)
	}

	if changed {
		s.revision++
	}
	result := MutationResult{Key: name, Revision: s.revision, Changed: changed}
	if idempotencyKey != "" {
		s.idempotency[idempotencyKey] = storedIdempotency{
			Operation: request.Operation,
			Name:      name,
			Value:     request.Value,
			Result:    result,
		}
	}
	return result, nil
}

func (s *Scope) Destroy() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.values = nil
	s.idempotency = nil
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxDynamicKeys <= 0 {
		limits.MaxDynamicKeys = 32
	}
	if limits.MaxValueBytes <= 0 {
		limits.MaxValueBytes = 4096
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = 32768
	}
	return limits
}
