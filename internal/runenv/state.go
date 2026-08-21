package runenv

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type Identity struct {
	RunID    string
	ChatID   string
	Subject  string
	Owner    string
	AgentKey string
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
	Fingerprint string         `json:"fingerprint"`
	Result      MutationResult `json:"result"`
}

type State struct {
	mu          sync.RWMutex
	identity    Identity
	limits      Limits
	values      map[string][]byte
	revision    uint64
	closed      bool
	idempotency map[string]storedIdempotency
	store       *Store
	secret      []byte
}

func newState(identity Identity, limits Limits, store *Store, secret []byte) *State {
	return &State{
		identity: identity, limits: normalizeLimits(limits), values: map[string][]byte{},
		idempotency: map[string]storedIdempotency{}, store: store, secret: append([]byte(nil), secret...),
	}
}

func (s *State) Revision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *State) Snapshot() (map[string]string, uint64, error) {
	if s == nil {
		return map[string]string{}, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, s.revision, ErrClosed
	}
	out := make(map[string]string, len(s.values))
	for name, value := range s.values {
		out[name] = string(value)
	}
	return out, s.revision, nil
}

func (s *State) Mutate(request MutationRequest) (MutationResult, error) {
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
	fingerprint, idempotencyID := s.requestFingerprints(request, name)
	if idempotencyID != "" {
		if previous, ok := s.idempotency[idempotencyID]; ok {
			if !hmac.Equal([]byte(previous.Fingerprint), []byte(fingerprint)) {
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

	next := cloneValues(s.values)
	changed := false
	switch request.Operation {
	case OperationSet:
		if err := ValidateValue(request.Value, s.limits.MaxValueBytes); err != nil {
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("%s: %w", name, err)
		}
		if previous, exists := next[name]; !exists || !bytes.Equal(previous, []byte(request.Value)) {
			if exists {
				zero(previous)
			}
			next[name] = []byte(request.Value)
			changed = true
		}
	case OperationUnset:
		previous, exists := next[name]
		if !exists {
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("%w: %s", ErrKeyNotSet, name)
		}
		zero(previous)
		delete(next, name)
		changed = true
	default:
		zeroValues(next)
		return MutationResult{}, fmt.Errorf("unsupported run environment mutation %q", request.Operation)
	}
	if len(next) > s.limits.MaxDynamicKeys {
		zeroValues(next)
		return MutationResult{}, fmt.Errorf("run environment exceeds %d dynamic keys", s.limits.MaxDynamicKeys)
	}
	total := 0
	for _, value := range next {
		total += len(value)
	}
	if total > s.limits.MaxTotalBytes {
		zeroValues(next)
		return MutationResult{}, fmt.Errorf("run environment exceeds %d total bytes", s.limits.MaxTotalBytes)
	}
	nextRevision := s.revision
	if changed {
		nextRevision++
	}
	result := MutationResult{Key: name, Revision: nextRevision, Changed: changed}
	nextIdempotency := cloneIdempotency(s.idempotency)
	if idempotencyID != "" {
		nextIdempotency[idempotencyID] = storedIdempotency{Fingerprint: fingerprint, Result: result}
	}
	if changed || idempotencyID != "" {
		if err := s.store.save(s.identity, nextRevision, next, nextIdempotency); err != nil {
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("checkpoint run environment: %w", err)
		}
	}
	zeroValues(s.values)
	s.values = next
	s.revision = nextRevision
	s.idempotency = nextIdempotency
	return result, nil
}

func (s *State) Destroy() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	zeroValues(s.values)
	s.values = nil
	zero(s.secret)
	s.secret = nil
	store := s.store
	identity := s.identity
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.release(identity, s)
}

func (s *State) requestFingerprints(request MutationRequest, normalizedName string) (string, string) {
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		key = strings.TrimSpace(request.DefaultIdempotencyKey)
	}
	if key == "" || len(s.secret) == 0 {
		return "", ""
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("id:" + key))
	id := hex.EncodeToString(mac.Sum(nil))
	payload, _ := json.Marshal(struct {
		Operation Operation `json:"operation"`
		Name      string    `json:"name"`
		Value     string    `json:"value,omitempty"`
	}{Operation: request.Operation, Name: normalizedName, Value: request.Value})
	mac.Reset()
	mac.Write([]byte("args:"))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), id
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

func cloneValues(values map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(values))
	for name, value := range values {
		out[name] = append([]byte(nil), value...)
	}
	return out
}

func zeroValues(values map[string][]byte) {
	for _, value := range values {
		zero(value)
	}
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func cloneIdempotency(values map[string]storedIdempotency) map[string]storedIdempotency {
	out := make(map[string]storedIdempotency, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
