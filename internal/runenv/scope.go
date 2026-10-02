package runenv

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	OperationSet    Operation = "set"
	OperationUnset  Operation = "unset"
	OperationUpdate Operation = "update"
)

type MutationRequest struct {
	Operation             Operation
	Name                  string
	Set                   map[string]string
	Unset                 []string
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

// MaxIdempotencyRecords bounds receipts without evicting successful requests.
// At capacity, new mutations fail before changing state; retries still work.
const MaxIdempotencyRecords = 4096

var ErrIdempotencyLimit = errors.New("run environment idempotency record limit reached")
var ErrIdempotencyConflict = errors.New("idempotency key was already used with different arguments")

type storedIdempotency struct {
	Digest [32]byte
	Result MutationResult
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

	set := map[string]string{}
	unset := []string{}
	name := ""
	switch request.Operation {
	case OperationSet:
		name = NormalizeName(request.Name)
		set[name] = request.Value
	case OperationUnset:
		name = NormalizeName(request.Name)
		unset = append(unset, name)
	case OperationUpdate:
		for raw, value := range request.Set {
			key := NormalizeName(raw)
			if _, exists := set[key]; exists {
				return MutationResult{}, fmt.Errorf("duplicate normalized key %s", key)
			}
			set[key] = value
		}
		for _, raw := range request.Unset {
			unset = append(unset, NormalizeName(raw))
		}
		if len(set) == 0 && len(unset) == 0 {
			return MutationResult{}, fmt.Errorf("update must contain at least one set or unset key")
		}
	default:
		return MutationResult{}, fmt.Errorf("unsupported run environment mutation %q", request.Operation)
	}
	seen := map[string]bool{}
	for key, value := range set {
		if err := ValidateName(key, s.limits.ExtraDeniedKeys); err != nil {
			return MutationResult{}, err
		}
		if err := ValidateValue(value, s.limits.MaxValueBytes); err != nil {
			return MutationResult{}, err
		}
		seen[key] = true
	}
	for _, key := range unset {
		if err := ValidateName(key, s.limits.ExtraDeniedKeys); err != nil {
			return MutationResult{}, err
		}
		if seen[key] {
			return MutationResult{}, fmt.Errorf("duplicate or overlapping normalized key %s", key)
		}
		seen[key] = true
	}
	sort.Strings(unset)
	// encoding/json sorts map keys. Include the revision precondition, including
	// its absence, but never the idempotency key itself.
	canonical, err := json.Marshal(struct {
		Operation        Operation
		Set              map[string]string
		Unset            []string
		ExpectedRevision *uint64
	}{request.Operation, set, unset, request.ExpectedRevision})
	if err != nil {
		return MutationResult{}, err
	}
	digest := sha256.Sum256(canonical)
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = strings.TrimSpace(request.DefaultIdempotencyKey)
	}
	if idempotencyKey != "" {
		if len(idempotencyKey) > 256 {
			return MutationResult{}, fmt.Errorf("idempotency key is too long")
		}
		if previous, ok := s.idempotency[idempotencyKey]; ok {
			if previous.Digest != digest {
				return MutationResult{}, ErrIdempotencyConflict
			}
			result := previous.Result
			result.Idempotent = true
			return result, nil
		}
	}
	if request.ExpectedRevision != nil && *request.ExpectedRevision != s.revision {
		return MutationResult{}, fmt.Errorf("%w: expected %d, current %d", ErrRevisionConflict, *request.ExpectedRevision, s.revision)
	}
	candidate := make(map[string]string, len(s.values)+len(set))
	for key, value := range s.values {
		candidate[key] = value
	}
	changed := false
	for _, key := range unset {
		if _, exists := candidate[key]; !exists {
			return MutationResult{}, fmt.Errorf("%w: %s", ErrKeyNotSet, key)
		}
		delete(candidate, key)
		changed = true
	}
	for key, value := range set {
		if old, exists := candidate[key]; !exists || old != value {
			changed = true
		}
		candidate[key] = value
	}
	if len(candidate) > s.limits.MaxDynamicKeys {
		return MutationResult{}, fmt.Errorf("run environment exceeds %d dynamic keys", s.limits.MaxDynamicKeys)
	}
	total := 0
	for _, value := range candidate {
		total += len(value)
	}
	if total > s.limits.MaxTotalBytes {
		return MutationResult{}, fmt.Errorf("run environment exceeds %d total bytes", s.limits.MaxTotalBytes)
	}
	if idempotencyKey != "" && len(s.idempotency) >= MaxIdempotencyRecords {
		return MutationResult{}, ErrIdempotencyLimit
	}
	if changed {
		s.values = candidate
		s.revision++
	}
	result := MutationResult{Key: name, Revision: s.revision, Changed: changed}
	if idempotencyKey != "" {
		s.idempotency[idempotencyKey] = storedIdempotency{Digest: digest, Result: result}
	}
	return result, nil
}

// Inspect returns a coherent dynamic-only view; byte usage excludes key names.
func (s *Scope) Inspect() (map[string]any, error) {
	if s == nil {
		return nil, ErrClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	values := make(map[string]string, len(s.values))
	total := 0
	for key, value := range s.values {
		values[key] = value
		total += len(value)
	}
	return map[string]any{"variables": values, "revision": s.revision,
		"usage":  map[string]any{"dynamicKeys": len(values), "totalBytes": total, "idempotencyRecords": len(s.idempotency)},
		"limits": map[string]any{"maxDynamicKeys": s.limits.MaxDynamicKeys, "maxValueBytes": s.limits.MaxValueBytes, "maxTotalBytes": s.limits.MaxTotalBytes, "maxIdempotencyRecords": MaxIdempotencyRecords}}, nil
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
	limits.ExtraDeniedKeys = append([]string(nil), limits.ExtraDeniedKeys...)
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
