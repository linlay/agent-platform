package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const OrderFileName = "order.json"
const MaxPinnedSkills = 4096

type OrderState struct {
	Order     []string `json:"order"`
	UpdatedAt int64    `json:"updatedAt"`
}

type orderFile struct {
	Version int                   `json:"version"`
	Users   map[string]OrderState `json:"users"`
}

// FileOrderStore owns user-level pins shared by every Agent. The catalog itself
// is unchanged: skills absent from a particular picker simply do not appear.
type FileOrderStore struct {
	path string
	mu   sync.Mutex
}

func NewFileOrderStore(skillsCenterDir string) *FileOrderStore {
	return &FileOrderStore{path: filepath.Join(skillsCenterDir, OrderFileName)}
}

func (s *FileOrderStore) Read(user string) (OrderState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readLocked()
	if err != nil {
		return OrderState{}, err
	}
	return cloneOrder(file.Users[user]), nil
}

// SetPinned is idempotent and changes a single key under the file transaction
// lock, so concurrent clients do not replace each other's complete pin lists.
func (s *FileOrderStore) SetPinned(user, key string, pinned bool) (OrderState, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || len(key) > 256 || strings.ContainsAny(key, "/\\\x00\r\n") {
		return OrderState{}, fmt.Errorf("invalid skill key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readLocked()
	if err != nil {
		return OrderState{}, err
	}
	current := cloneOrder(file.Users[user])
	exists := false
	for _, item := range current.Order {
		if item == key {
			exists = true
			break
		}
	}
	if exists == pinned {
		return current, nil
	}
	next := make([]string, 0, len(current.Order)+1)
	if pinned {
		if len(current.Order) >= MaxPinnedSkills {
			return OrderState{}, fmt.Errorf("pinned skill limit reached")
		}
		next = append(next, key)
	}
	for _, item := range current.Order {
		if item != key {
			next = append(next, item)
		}
	}
	current = OrderState{Order: next, UpdatedAt: time.Now().UnixMilli()}
	file.Users[user] = current
	if err := s.writeLocked(file); err != nil {
		return OrderState{}, err
	}
	return cloneOrder(current), nil
}

func cloneOrder(state OrderState) OrderState {
	state.Order = append([]string{}, state.Order...)
	return state
}

func (s *FileOrderStore) readLocked() (orderFile, error) {
	file := orderFile{Version: 1, Users: map[string]OrderState{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return file, fmt.Errorf("decode skill order: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return file, fmt.Errorf("skill order must contain one JSON value")
	}
	if file.Version != 1 || file.Users == nil {
		return file, fmt.Errorf("unsupported skill order file")
	}
	for _, state := range file.Users {
		if state.UpdatedAt <= 0 || len(state.Order) > MaxPinnedSkills {
			return file, fmt.Errorf("invalid skill order state")
		}
		seen := map[string]bool{}
		for _, key := range state.Order {
			if key == "" || len(key) > 256 || key != strings.ToLower(strings.TrimSpace(key)) || strings.ContainsAny(key, "/\\\x00\r\n") || seen[key] {
				return file, fmt.Errorf("invalid pinned skill key")
			}
			seen[key] = true
		}
	}
	return file, nil
}

func (s *FileOrderStore) writeLocked(file orderFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".skill-order-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
