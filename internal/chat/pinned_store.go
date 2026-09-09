package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const ChatPinnedFileName = "chat-pinned.json"

// PinnedState is an instance-wide presentation preference, independent of
// manual chat ordering and the conversation database.
type PinnedState struct {
	Version   int      `json:"version"`
	Order     []string `json:"order"`
	UpdatedAt int64    `json:"updatedAt"`
}

type PinnedStore interface {
	ChatPinned() (PinnedState, error)
	SetChatPinned(chatID string, pinned bool) (PinnedState, bool, error)
}

type ListOptions struct {
	OwnerOnly  bool
	LastRunID  string
	AgentKey   string
	TeamID     string
	AgentModes []string
	Pinned     *bool
	Limit      int
}

type PinnedListStore interface {
	ListChatsWithOptions(ListOptions) ([]Summary, error)
	RecentChatsByOwner(agentKey, teamID string, limit int, pinned *bool) ([]Summary, error)
}

func defaultPinnedState() PinnedState {
	return PinnedState{Version: 1, Order: []string{}}
}

func (s *FileStore) readChatPinnedLocked() (PinnedState, error) {
	data, err := os.ReadFile(filepath.Join(s.root, ChatPinnedFileName))
	if errors.Is(err, os.ErrNotExist) {
		return defaultPinnedState(), nil
	}
	if err != nil {
		return PinnedState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state PinnedState
	if err := decoder.Decode(&state); err != nil {
		return PinnedState{}, fmt.Errorf("decode chat pins: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PinnedState{}, errors.New("chat pins must contain exactly one JSON value")
	}
	if state.Version != 1 || state.UpdatedAt < 0 {
		return PinnedState{}, errors.New("invalid chat pins version or updatedAt")
	}
	seen := make(map[string]bool, len(state.Order))
	for _, id := range state.Order {
		if !ValidChatID(id) || id != strings.TrimSpace(id) || seen[id] {
			return PinnedState{}, errors.New("chat pins contain invalid or duplicate chat id")
		}
		seen[id] = true
	}
	if state.Order == nil {
		state.Order = []string{}
	}
	return state, nil
}

// A damaged preference must not prevent conversation execution or replay.
// Mutations use the strict reader and never overwrite damaged preferences.
func (s *FileStore) readChatPinnedForListLocked() PinnedState {
	state, err := s.readChatPinnedLocked()
	if err != nil {
		log.Printf("chat pins: ignoring invalid presentation preferences: %v", err)
		return defaultPinnedState()
	}
	return state
}

func (s *FileStore) ChatPinned() (PinnedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatPinnedLocked()
	if err != nil {
		return PinnedState{}, err
	}
	return s.activeChatPinsLocked(state)
}

func (s *FileStore) activeChatPinsLocked(state PinnedState) (PinnedState, error) {
	active, err := s.listAllChatIDsRecentLocked()
	if err != nil {
		return PinnedState{}, err
	}
	ids := make(map[string]bool, len(active))
	for _, id := range active {
		ids[id] = true
	}
	state.Order = slices.DeleteFunc(state.Order, func(id string) bool { return !ids[id] })
	return state, nil
}

func (s *FileStore) SetChatPinned(chatID string, pinned bool) (PinnedState, bool, error) {
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return PinnedState{}, false, &OrderValidationError{Message: "invalid chatId"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatPinnedLocked()
	if err != nil {
		return PinnedState{}, false, err
	}
	if pinned {
		summary, err := s.loadSummary(chatID)
		if err != nil {
			return PinnedState{}, false, err
		}
		if summary == nil {
			return PinnedState{}, false, ErrChatNotFound
		}
		if isPendingChatName(summary.ChatName) && strings.TrimSpace(summary.LastRunID) == "" {
			return PinnedState{}, false, &OrderValidationError{Message: "pending upload chat cannot be pinned"}
		}
	}
	if containsChatID(state.Order, chatID) == pinned {
		return state, false, nil
	}
	if pinned {
		state.Order = append([]string{chatID}, state.Order...)
	} else {
		state.Order = slices.DeleteFunc(state.Order, func(id string) bool { return id == chatID })
	}
	state, err = s.activeChatPinsLocked(state)
	if err != nil {
		return PinnedState{}, false, err
	}
	if err := s.writeChatPinnedLocked(&state); err != nil {
		return PinnedState{}, false, err
	}
	return state, true, nil
}

// Cleanup happens before deletion/restore so a crash cannot revive an old pin
// when the same Chat ID becomes active again. It only touches preferences.
func (s *FileStore) clearChatPinnedLocked(chatID string) error {
	state, err := s.readChatPinnedLocked()
	if err != nil {
		return err
	}
	if !containsChatID(state.Order, chatID) {
		return nil
	}
	state.Order = slices.DeleteFunc(state.Order, func(id string) bool { return id == chatID })
	return s.writeChatPinnedLocked(&state)
}

func (s *FileStore) writeChatPinnedLocked(state *PinnedState) error {
	state.UpdatedAt = max(time.Now().UnixMilli(), state.UpdatedAt+1)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".chat-pinned-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return atomicReplaceFile(tmp.Name(), filepath.Join(s.root, ChatPinnedFileName))
}

func applyChatPins(items []Summary, state PinnedState, filter *bool, pinnedFirst bool) []Summary {
	byID := make(map[string]Summary)
	remaining := make([]Summary, 0, len(items))
	pins := make(map[string]bool, len(state.Order))
	for _, id := range state.Order {
		pins[id] = true
	}
	for _, item := range items {
		item.Pinned = pins[item.ChatID]
		if filter != nil && item.Pinned != *filter {
			continue
		}
		if pinnedFirst && item.Pinned {
			byID[item.ChatID] = item
		} else {
			remaining = append(remaining, item)
		}
	}
	if !pinnedFirst {
		return remaining
	}
	ordered := make([]Summary, 0, len(byID)+len(remaining))
	for _, id := range state.Order {
		if item, ok := byID[id]; ok {
			ordered = append(ordered, item)
		}
	}
	return append(ordered, remaining...)
}
