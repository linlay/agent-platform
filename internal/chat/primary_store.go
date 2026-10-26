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
	"strings"
	"time"
)

const ChatPrimaryFileName = "chat-primary.json"

// PrimaryEntry points an Agent at its primary Chat. The other active Chats of
// that Agent are its history Chats.
type PrimaryEntry struct {
	ChatID string `json:"chatId"`
	Since  int64  `json:"since"`
}

// PrimaryState is an instance-wide presentation preference kept beside the
// pin and order preferences, outside the conversation database.
type PrimaryState struct {
	Version   int                     `json:"version"`
	Agents    map[string]PrimaryEntry `json:"agents"`
	UpdatedAt int64                   `json:"updatedAt"`
}

type PrimaryChange struct {
	AgentKey       string
	ChatID         string
	PreviousChatID string
	Changed        bool
}

type PrimaryStore interface {
	// PrimaryChatID returns "" when the Agent has no primary Chat or its
	// pointer no longer names an active Chat of that Agent.
	PrimaryChatID(agentKey string) (string, error)
	SetChatPrimary(agentKey string, chatID string) (PrimaryChange, error)
}

// PrimaryChatBusyError rejects replacing a primary Chat that still has a Run.
type PrimaryChatBusyError struct {
	AgentKey string
	ChatID   string
	RunIDs   []string
}

func (e *PrimaryChatBusyError) Error() string {
	return "the current primary chat has an active run"
}

// IsPendingUploadChat reports a Chat allocated by an upload before its first
// accepted query. It is addressable by ID but not yet published as history.
func IsPendingUploadChat(summary Summary) bool {
	return isPendingChatName(summary.ChatName) && strings.TrimSpace(summary.LastRunID) == ""
}

func defaultPrimaryState() PrimaryState {
	return PrimaryState{Version: 1, Agents: map[string]PrimaryEntry{}}
}

func (s *FileStore) readChatPrimaryLocked() (PrimaryState, error) {
	data, err := os.ReadFile(filepath.Join(s.root, ChatPrimaryFileName))
	if errors.Is(err, os.ErrNotExist) {
		return defaultPrimaryState(), nil
	}
	if err != nil {
		return PrimaryState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state PrimaryState
	if err := decoder.Decode(&state); err != nil {
		return PrimaryState{}, fmt.Errorf("decode primary chats: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PrimaryState{}, errors.New("primary chats must contain exactly one JSON value")
	}
	if state.Version != 1 || state.UpdatedAt < 0 {
		return PrimaryState{}, errors.New("invalid primary chats version or updatedAt")
	}
	seen := make(map[string]bool, len(state.Agents))
	for agentKey, entry := range state.Agents {
		if agentKey == "" || agentKey != strings.TrimSpace(agentKey) {
			return PrimaryState{}, errors.New("primary chats contain an invalid agent key")
		}
		if !ValidChatID(entry.ChatID) || entry.ChatID != strings.TrimSpace(entry.ChatID) || entry.Since < 0 || seen[entry.ChatID] {
			return PrimaryState{}, errors.New("primary chats contain an invalid or duplicate chat id")
		}
		seen[entry.ChatID] = true
	}
	if state.Agents == nil {
		state.Agents = map[string]PrimaryEntry{}
	}
	return state, nil
}

// A damaged preference must not prevent listing or replay. Mutations use the
// strict reader and never overwrite a damaged file.
func (s *FileStore) readChatPrimaryForListLocked() PrimaryState {
	state, err := s.readChatPrimaryLocked()
	if err != nil {
		log.Printf("primary chats: ignoring invalid presentation preferences: %v", err)
		return defaultPrimaryState()
	}
	return state
}

func (s *FileStore) PrimaryChatID(agentKey string) (string, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatPrimaryLocked()
	if err != nil {
		return "", err
	}
	entry, ok := state.Agents[agentKey]
	if !ok {
		return "", nil
	}
	summary, err := s.loadSummary(entry.ChatID)
	if err != nil {
		return "", err
	}
	if summary == nil || summary.AgentKey != agentKey {
		return "", nil
	}
	return entry.ChatID, nil
}

// SetChatPrimary points agentKey at chatID. The Chat must be active and owned
// by that Agent; callers decide whether the previous primary may be replaced.
func (s *FileStore) SetChatPrimary(agentKey string, chatID string) (PrimaryChange, error) {
	agentKey = strings.TrimSpace(agentKey)
	chatID = strings.TrimSpace(chatID)
	change := PrimaryChange{AgentKey: agentKey, ChatID: chatID}
	if agentKey == "" {
		return change, &OrderValidationError{Message: "agentKey is required"}
	}
	if !ValidChatID(chatID) {
		return change, &OrderValidationError{Message: "invalid chatId"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatPrimaryLocked()
	if err != nil {
		return change, err
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return change, err
	}
	if summary == nil {
		return change, ErrChatNotFound
	}
	if summary.AgentKey != agentKey {
		return change, &OrderValidationError{Message: "chat does not belong to the agent"}
	}
	previous := state.Agents[agentKey]
	change.PreviousChatID = previous.ChatID
	if previous.ChatID == chatID {
		return change, nil
	}
	state.Agents[agentKey] = PrimaryEntry{ChatID: chatID, Since: time.Now().UnixMilli()}
	if err := s.writeChatPrimaryLocked(&state); err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}

// Cleanup happens before deletion/restore so a crash cannot revive an old
// pointer when the same Chat ID becomes active again.
func (s *FileStore) clearChatPrimaryLocked(chatID string) error {
	state, err := s.readChatPrimaryLocked()
	if err != nil {
		return err
	}
	changed := false
	for agentKey, entry := range state.Agents {
		if entry.ChatID == chatID {
			delete(state.Agents, agentKey)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.writeChatPrimaryLocked(&state)
}

func (s *FileStore) writeChatPrimaryLocked(state *PrimaryState) error {
	state.UpdatedAt = max(time.Now().UnixMilli(), state.UpdatedAt+1)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".chat-primary-*.json")
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
	return atomicReplaceFile(tmp.Name(), filepath.Join(s.root, ChatPrimaryFileName))
}

// primaryOwners maps each primary chat ID to the Agent that points at it.
func (state PrimaryState) primaryOwners() map[string]string {
	owners := make(map[string]string, len(state.Agents))
	for agentKey, entry := range state.Agents {
		owners[entry.ChatID] = agentKey
	}
	return owners
}

// isPrimary also requires the Chat's owner to match, so a stale pointer never
// marks a Chat that moved to another Agent.
func isPrimary(owners map[string]string, chatID string, agentKey string) bool {
	owner, ok := owners[chatID]
	return ok && owner == agentKey
}

func primaryChatIDs(owners map[string]string) []string {
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	return ids
}
