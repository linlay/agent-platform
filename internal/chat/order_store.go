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

const ChatOrderFileName = "chat-order.json"

type SortMode string

const (
	SortModeRecent SortMode = "recent"
	SortModeManual SortMode = "manual"
)

type OrderState struct {
	Version   int      `json:"version"`
	SortMode  SortMode `json:"sortMode"`
	Order     []string `json:"order"`
	UpdatedAt int64    `json:"updatedAt"`
}

// OrderStore is optional so lightweight Store test doubles do not need to
// implement durable chat ordering. The production FileStore always does.
type OrderStore interface {
	ChatOrder() (OrderState, error)
	SetChatSortMode(mode SortMode) (OrderState, error)
	MoveChat(chatID string, beforeChatID string, afterChatID string) (OrderState, error)
}

type OrderValidationError struct {
	Message string
}

func (e *OrderValidationError) Error() string { return e.Message }

func defaultOrderState() OrderState {
	return OrderState{Version: 1, SortMode: SortModeRecent, Order: []string{}}
}

func (s *FileStore) chatOrderPath() string {
	return filepath.Join(s.root, ChatOrderFileName)
}

func (s *FileStore) ChatOrder() (OrderState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatOrderLocked()
	if err != nil {
		log.Printf("chat order: ignoring %s and falling back to recent: %v", s.chatOrderPath(), err)
		return defaultOrderState(), nil
	}
	return state, nil
}

func (s *FileStore) SetChatSortMode(mode SortMode) (OrderState, error) {
	if mode != SortModeRecent && mode != SortModeManual {
		return OrderState{}, &OrderValidationError{Message: "sortMode must be recent or manual"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readChatOrderLocked()
	if err != nil {
		log.Printf("chat order: rebuilding invalid state before set_mode: %v", err)
		state = defaultOrderState()
	}
	recent, err := s.listAllChatIDsRecentLocked()
	if err != nil {
		return OrderState{}, err
	}
	state.Version = 1
	state.SortMode = mode
	state.Order = orderChatIDs(recent, state.Order)
	state.UpdatedAt = time.Now().UnixMilli()
	if err := s.writeChatOrderLocked(state); err != nil {
		return OrderState{}, err
	}
	return cloneOrderState(state), nil
}

func (s *FileStore) MoveChat(chatID string, beforeChatID string, afterChatID string) (OrderState, error) {
	chatID = strings.TrimSpace(chatID)
	beforeChatID = strings.TrimSpace(beforeChatID)
	afterChatID = strings.TrimSpace(afterChatID)
	if chatID == "" {
		return OrderState{}, &OrderValidationError{Message: "chatId is required"}
	}
	if (beforeChatID == "") == (afterChatID == "") {
		return OrderState{}, &OrderValidationError{Message: "exactly one of beforeChatId or afterChatId is required"}
	}
	anchorID := beforeChatID
	if anchorID == "" {
		anchorID = afterChatID
	}
	if chatID == anchorID {
		return OrderState{}, &OrderValidationError{Message: "chatId cannot be anchored to itself"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readChatOrderLocked()
	if err != nil {
		log.Printf("chat order: rebuilding invalid state before move: %v", err)
		state = defaultOrderState()
	}
	recent, err := s.listAllChatIDsRecentLocked()
	if err != nil {
		return OrderState{}, err
	}
	baseline := append([]string(nil), recent...)
	if state.SortMode == SortModeManual {
		baseline = orderChatIDs(recent, state.Order)
	}
	if !containsChatID(baseline, chatID) {
		return OrderState{}, &OrderValidationError{Message: fmt.Sprintf("unknown active chat: %s", chatID)}
	}
	if !containsChatID(baseline, anchorID) {
		return OrderState{}, &OrderValidationError{Message: fmt.Sprintf("unknown active anchor chat: %s", anchorID)}
	}

	withoutMoved := make([]string, 0, len(baseline)-1)
	for _, id := range baseline {
		if id != chatID {
			withoutMoved = append(withoutMoved, id)
		}
	}
	anchorIndex := -1
	for index, id := range withoutMoved {
		if id == anchorID {
			anchorIndex = index
			break
		}
	}
	insertAt := anchorIndex
	if afterChatID != "" {
		insertAt++
	}
	baseline = append(withoutMoved, "")
	copy(baseline[insertAt+1:], baseline[insertAt:])
	baseline[insertAt] = chatID

	state = OrderState{
		Version:   1,
		SortMode:  SortModeManual,
		Order:     baseline,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := s.writeChatOrderLocked(state); err != nil {
		return OrderState{}, err
	}
	return cloneOrderState(state), nil
}

func (s *FileStore) readChatOrderForListLocked() OrderState {
	state, err := s.readChatOrderLocked()
	if err != nil {
		log.Printf("chat order: ignoring %s and falling back to recent: %v", s.chatOrderPath(), err)
		return defaultOrderState()
	}
	return state
}

func (s *FileStore) readChatOrderLocked() (OrderState, error) {
	data, err := os.ReadFile(s.chatOrderPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultOrderState(), nil
	}
	if err != nil {
		return OrderState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state OrderState
	if err := decoder.Decode(&state); err != nil {
		return OrderState{}, fmt.Errorf("decode chat order: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return OrderState{}, fmt.Errorf("chat order must contain exactly one JSON value")
	}
	if state.Version != 1 {
		return OrderState{}, fmt.Errorf("unsupported chat order version: %d", state.Version)
	}
	if state.SortMode != SortModeRecent && state.SortMode != SortModeManual {
		return OrderState{}, fmt.Errorf("invalid chat sort mode: %q", state.SortMode)
	}
	if state.UpdatedAt < 0 {
		return OrderState{}, fmt.Errorf("updatedAt must be an epoch-millisecond integer")
	}
	seen := make(map[string]struct{}, len(state.Order))
	for _, raw := range state.Order {
		id := strings.TrimSpace(raw)
		if id == "" || id != raw {
			return OrderState{}, fmt.Errorf("order contains an invalid chat id")
		}
		if _, exists := seen[id]; exists {
			return OrderState{}, fmt.Errorf("order contains duplicate chat id: %s", id)
		}
		seen[id] = struct{}{}
	}
	if state.Order == nil {
		state.Order = []string{}
	}
	return state, nil
}

func (s *FileStore) writeChatOrderLocked(state OrderState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.root, ".chat-order-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	return atomicReplaceFile(tmpPath, s.chatOrderPath())
}

func (s *FileStore) listAllChatIDsRecentLocked() ([]string, error) {
	rows, err := s.db.Query("SELECT CHAT_ID_ FROM CHATS ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func orderChatIDs(recent []string, saved []string) []string {
	active := make(map[string]struct{}, len(recent))
	for _, id := range recent {
		active[id] = struct{}{}
	}
	known := make(map[string]struct{}, len(saved))
	for _, id := range saved {
		if _, ok := active[id]; ok {
			known[id] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(recent))
	for _, id := range recent {
		if _, ok := known[id]; !ok {
			ordered = append(ordered, id)
		}
	}
	seen := make(map[string]struct{}, len(saved))
	for _, id := range saved {
		if _, ok := active[id]; !ok {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	return ordered
}

func orderSummaries(items []Summary, saved []string) []Summary {
	byID := make(map[string]Summary, len(items))
	recent := make([]string, 0, len(items))
	for _, item := range items {
		byID[item.ChatID] = item
		recent = append(recent, item.ChatID)
	}
	ids := orderChatIDs(recent, saved)
	ordered := make([]Summary, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, byID[id])
	}
	return ordered
}

func containsChatID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func cloneOrderState(state OrderState) OrderState {
	state.Order = append([]string(nil), state.Order...)
	return state
}
