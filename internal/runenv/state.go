package runenv

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
)

type Limits struct {
	MaxDynamicKeys    int
	MaxValueBytes     int
	MaxTotalBytes     int
	MaxBulkOperations int
	ExtraDeniedKeys   []string
}

type Identity struct {
	RunID      string
	ChatID     string
	Subject    string
	Owner      string
	AgentKey   string
	PolicyHash string
}

type Operation string

const (
	OperationBind  Operation = "bind"
	OperationSet   Operation = "set"
	OperationUnset Operation = "unset"
)

type Mutation struct {
	Operation Operation
	Name      string
	Value     string
}

type MutationRequest struct {
	Operations            []Mutation
	ExpectedRevision      *uint64
	IdempotencyKey        string
	DefaultIdempotencyKey string
}

type MutationResult struct {
	Revision   uint64
	Changed    bool
	Idempotent bool
	Items      []Metadata
}

type Metadata struct {
	Name     string   `json:"name"`
	Present  bool     `json:"present"`
	Mode     Mode     `json:"mode"`
	Source   string   `json:"source"`
	Secret   bool     `json:"secret"`
	Targets  []Target `json:"targets"`
	Revision uint64   `json:"revision"`
}

type storedIdempotency struct {
	Fingerprint string         `json:"fingerprint"`
	Result      MutationResult `json:"result"`
}

type State struct {
	mu          sync.RWMutex
	identity    Identity
	policy      Policy
	limits      Limits
	values      map[string][]byte
	revision    uint64
	closed      bool
	idempotency map[string]storedIdempotency
	store       *Store
	secret      []byte
}

func newState(identity Identity, policy Policy, limits Limits, store *Store, secret []byte) *State {
	identity.PolicyHash = policy.Hash()
	return &State{
		identity: identity, policy: policy, limits: normalizeLimits(limits), values: map[string][]byte{},
		idempotency: map[string]storedIdempotency{}, store: store, secret: append([]byte(nil), secret...),
	}
}

func (s *State) Policy() Policy {
	if s == nil {
		return Policy{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *State) Revision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *State) Snapshot(target Target, consumer Policy) (map[string]string, uint64, error) {
	if s == nil {
		return nil, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, s.revision, ErrClosed
	}
	out := map[string]string{}
	for name, value := range s.values {
		ownerPolicy, ok := s.policy.Key(name)
		if !ok || !ownerPolicy.AllowsTarget(target) {
			continue
		}
		consumerPolicy, allowed := consumer.Key(name)
		if !allowed || !consumerPolicy.AllowsTarget(target) {
			continue
		}
		out[name] = string(value)
	}
	return out, s.revision, nil
}

func (s *State) List(static map[string]string) ([]Metadata, uint64, error) {
	if s == nil {
		return nil, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, s.revision, ErrClosed
	}
	names := make([]string, 0, len(s.policy.Keys))
	for name := range s.policy.Keys {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]Metadata, 0, len(names))
	for _, name := range names {
		item := s.policy.Keys[name]
		_, dynamic := s.values[name]
		_, base := lookupStatic(static, name)
		source := "none"
		if base {
			source = "static"
		}
		if dynamic {
			source = "dynamic"
		}
		items = append(items, Metadata{
			Name: name, Present: dynamic || base, Mode: item.Mode, Source: source,
			Secret: item.Secret, Targets: append([]Target(nil), item.Targets...), Revision: s.revision,
		})
	}
	return items, s.revision, nil
}

func (s *State) Get(name string, static map[string]string) (Metadata, uint64, error) {
	if s == nil {
		return Metadata{}, 0, nil
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Metadata{}, s.revision, ErrClosed
	}
	item, ok := s.policy.Key(name)
	if !ok {
		return Metadata{}, s.revision, fmt.Errorf("run environment key is not configured: %s", name)
	}
	_, dynamic := s.values[name]
	_, base := lookupStatic(static, name)
	source := "none"
	if base {
		source = "static"
	}
	if dynamic {
		source = "dynamic"
	}
	return Metadata{Name: name, Present: dynamic || base, Mode: item.Mode, Source: source, Secret: item.Secret, Targets: append([]Target(nil), item.Targets...), Revision: s.revision}, s.revision, nil
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
	if len(request.Operations) == 0 {
		return MutationResult{}, fmt.Errorf("at least one mutation is required")
	}
	if len(request.Operations) > s.limits.MaxBulkOperations {
		return MutationResult{}, fmt.Errorf("bulk operation exceeds %d items", s.limits.MaxBulkOperations)
	}
	fingerprint, idempotencyID := s.requestFingerprints(request)
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
	seen := map[string]bool{}
	items := make([]Metadata, 0, len(request.Operations))
	for _, mutation := range request.Operations {
		name := strings.ToUpper(strings.TrimSpace(mutation.Name))
		if seen[name] {
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("bulk operation contains duplicate key %s", name)
		}
		seen[name] = true
		if err := ValidateName(name, s.limits.ExtraDeniedKeys); err != nil {
			zeroValues(next)
			return MutationResult{}, err
		}
		policy, ok := s.policy.Key(name)
		if !ok {
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("run environment key is not configured: %s", name)
		}
		switch mutation.Operation {
		case OperationBind:
			if policy.Mode != ModeBind {
				zeroValues(next)
				return MutationResult{}, fmt.Errorf("run environment key %s is not bind-only", name)
			}
			if err := policy.ValidateValue(mutation.Value, s.limits.MaxValueBytes); err != nil {
				zeroValues(next)
				return MutationResult{}, fmt.Errorf("%s: %w", name, err)
			}
			if previous, exists := next[name]; exists {
				if !bytes.Equal(previous, []byte(mutation.Value)) {
					zeroValues(next)
					return MutationResult{}, fmt.Errorf("run environment key %s is already bound", name)
				}
			} else {
				next[name] = []byte(mutation.Value)
				changed = true
			}
		case OperationSet:
			if policy.Mode != ModeMutable {
				zeroValues(next)
				return MutationResult{}, fmt.Errorf("run environment key %s is not mutable", name)
			}
			if err := policy.ValidateValue(mutation.Value, s.limits.MaxValueBytes); err != nil {
				zeroValues(next)
				return MutationResult{}, fmt.Errorf("%s: %w", name, err)
			}
			if previous, exists := next[name]; !exists || !bytes.Equal(previous, []byte(mutation.Value)) {
				if exists {
					zero(previous)
				}
				next[name] = []byte(mutation.Value)
				changed = true
			}
		case OperationUnset:
			if policy.Mode != ModeMutable {
				zeroValues(next)
				return MutationResult{}, fmt.Errorf("run environment key %s cannot be unset", name)
			}
			if previous, exists := next[name]; exists {
				zero(previous)
				delete(next, name)
				changed = true
			}
		default:
			zeroValues(next)
			return MutationResult{}, fmt.Errorf("unsupported run environment mutation %q", mutation.Operation)
		}
		items = append(items, Metadata{Name: name, Present: mutation.Operation != OperationUnset || next[name] != nil, Mode: policy.Mode, Source: "dynamic", Secret: policy.Secret, Targets: append([]Target(nil), policy.Targets...)})
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
	for index := range items {
		items[index].Revision = nextRevision
		if items[index].Source == "dynamic" {
			_, items[index].Present = next[items[index].Name]
			if !items[index].Present {
				items[index].Source = "none"
			}
		}
	}
	result := MutationResult{Revision: nextRevision, Changed: changed, Items: items}
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

func (s *State) requestFingerprints(request MutationRequest) (string, string) {
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
	payload, _ := json.Marshal(request.Operations)
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
	if limits.MaxBulkOperations <= 0 {
		limits.MaxBulkOperations = 16
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

func lookupStatic(values map[string]string, name string) (string, bool) {
	for key, value := range values {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}
