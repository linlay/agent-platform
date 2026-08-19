package runenv

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const checkpointVersion = 2

type Store struct {
	root    string
	keyFile string
	limits  Limits
	mu      sync.Mutex
	states  map[string]*State
}

type checkpoint struct {
	Version     int                          `json:"version"`
	Revision    uint64                       `json:"revision"`
	Values      map[string][]byte            `json:"values"`
	Idempotency map[string]storedIdempotency `json:"idempotency,omitempty"`
}

func NewStore(root string, keyFile string, limits Limits) *Store {
	return &Store{root: filepath.Clean(root), keyFile: filepath.Clean(keyFile), limits: normalizeLimits(limits), states: map[string]*State{}}
}

func (s *Store) NewScope(identity Identity) (*Scope, error) {
	if s == nil {
		return nil, fmt.Errorf("run environment store is unavailable")
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	return newScope(identity, s), nil
}

func (s *Store) RestoreScope(identity Identity, expectedRevision uint64) (*Scope, error) {
	if s == nil {
		return nil, fmt.Errorf("run environment store is unavailable")
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	if expectedRevision == 0 {
		if exists, err := s.HasCheckpoint(identity); err != nil {
			return nil, err
		} else if exists {
			return nil, fmt.Errorf("unexpected run environment checkpoint at revision 0")
		}
		return newScope(identity, s), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	indexKey := strings.TrimSpace(identity.RunID)
	if existing := s.states[indexKey]; existing != nil {
		return nil, fmt.Errorf("run environment state already exists: %s", indexKey)
	}
	key, err := s.readKey()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path(identity))
	if err != nil {
		zero(key)
		return nil, err
	}
	plain, err := decryptCheckpoint(key, raw, aad(identity))
	if err != nil {
		zero(key)
		return nil, fmt.Errorf("decrypt run environment checkpoint: %w", err)
	}
	defer zero(plain)
	var saved checkpoint
	if err := json.Unmarshal(plain, &saved); err != nil {
		zero(key)
		return nil, fmt.Errorf("decode run environment checkpoint: %w", err)
	}
	if saved.Version != checkpointVersion {
		zero(key)
		return nil, fmt.Errorf("unsupported run environment checkpoint version %d", saved.Version)
	}
	if saved.Revision != expectedRevision {
		zero(key)
		return nil, fmt.Errorf("run environment checkpoint revision mismatch: expected %d, found %d", expectedRevision, saved.Revision)
	}
	if err := s.validateValues(saved.Values); err != nil {
		zero(key)
		return nil, fmt.Errorf("validate run environment checkpoint: %w", err)
	}
	state := newState(identity, s.limits, s, key)
	zero(key)
	state.revision = saved.Revision
	state.values = cloneValues(saved.Values)
	state.idempotency = cloneIdempotency(saved.Idempotency)
	s.states[indexKey] = state
	scope := newScope(identity, s)
	scope.state = state
	return scope, nil
}

func (s *Store) HasCheckpoint(identity Identity) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("run environment store is unavailable")
	}
	if err := validateIdentity(identity); err != nil {
		return false, err
	}
	_, err := os.Stat(s.path(identity))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (s *Store) materialize(identity Identity) (*State, error) {
	if s == nil {
		return nil, fmt.Errorf("run environment store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	indexKey := strings.TrimSpace(identity.RunID)
	if existing := s.states[indexKey]; existing != nil {
		return nil, fmt.Errorf("run environment state already exists: %s", indexKey)
	}
	key, err := s.loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	state := newState(identity, s.limits, s, key)
	zero(key)
	s.states[indexKey] = state
	return state, nil
}

func (s *Store) validateValues(values map[string][]byte) error {
	if len(values) > s.limits.MaxDynamicKeys {
		return fmt.Errorf("run environment exceeds %d dynamic keys", s.limits.MaxDynamicKeys)
	}
	total := 0
	for rawName, value := range values {
		name := NormalizeName(rawName)
		if rawName != name {
			return fmt.Errorf("environment variable name %q is not canonical", rawName)
		}
		if err := ValidateName(name, s.limits.ExtraDeniedKeys); err != nil {
			return err
		}
		if err := ValidateValue(string(value), s.limits.MaxValueBytes); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		total += len(value)
	}
	if total > s.limits.MaxTotalBytes {
		return fmt.Errorf("run environment exceeds %d total bytes", s.limits.MaxTotalBytes)
	}
	return nil
}

func (s *Store) save(identity Identity, revision uint64, values map[string][]byte, idempotency map[string]storedIdempotency) error {
	if s == nil {
		return fmt.Errorf("run environment store is unavailable")
	}
	key, err := s.readKey()
	if err != nil {
		return err
	}
	defer zero(key)
	payload, err := json.Marshal(checkpoint{Version: checkpointVersion, Revision: revision, Values: values, Idempotency: idempotency})
	if err != nil {
		return err
	}
	defer zero(payload)
	encrypted, err := encryptCheckpoint(key, payload, aad(identity))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.root, ".run-env-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(encrypted); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempName, s.path(identity))
}

func (s *Store) remove(identity Identity) error {
	err := os.Remove(s.path(identity))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) release(identity Identity, state *State) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	key := strings.TrimSpace(identity.RunID)
	if current := s.states[key]; current == state {
		delete(s.states, key)
	}
	s.mu.Unlock()
	return s.remove(identity)
}

func (s *Store) path(identity Identity) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(identity.RunID)))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".checkpoint")
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	key, err := s.readKey()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.keyFile), 0o700); err != nil {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		zero(key)
		return s.readKey()
	}
	if err != nil {
		zero(key)
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		zero(key)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		zero(key)
		return nil, err
	}
	if err := file.Close(); err != nil {
		zero(key)
		return nil, err
	}
	return key, nil
}

func (s *Store) readKey() ([]byte, error) {
	key, err := os.ReadFile(s.keyFile)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		zero(key)
		return nil, fmt.Errorf("run environment checkpoint key must contain exactly 32 bytes")
	}
	return key, nil
}

func validateIdentity(identity Identity) error {
	if strings.TrimSpace(identity.RunID) == "" || strings.TrimSpace(identity.ChatID) == "" || strings.TrimSpace(identity.Owner) == "" {
		return fmt.Errorf("run environment identity requires runId, chatId, and owner")
	}
	return nil
}

func encryptCheckpoint(key, plain, aadValue []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plain, aadValue)...), nil
}

func decryptCheckpoint(key, encrypted, aadValue []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encrypted) < gcm.NonceSize() {
		return nil, fmt.Errorf("checkpoint is truncated")
	}
	nonce, ciphertext := encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, aadValue)
}

func aad(identity Identity) []byte {
	raw, _ := json.Marshal([]string{
		strings.TrimSpace(identity.RunID), strings.TrimSpace(identity.ChatID), strings.TrimSpace(identity.Subject),
		strings.TrimSpace(identity.Owner), strings.TrimSpace(identity.AgentKey),
	})
	return raw
}
