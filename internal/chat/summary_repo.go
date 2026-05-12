package chat

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"
)

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if exists
	var existing Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.TeamID, &existing.SourceChannel, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunID, &existing.LastRunContent, &existing.Read.ReadRunID, &existing.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if err == nil {
		applyDerivedReadState(&existing)
		if usage.TotalTokens > 0 {
			existing.Usage = &usage
		}
		existing.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		return existing, false, nil
	}

	now := time.Now().UnixMilli()
	summary := Summary{
		ChatID:    chatID,
		ChatName:  defaultChatName(firstMessage),
		AgentKey:  agentKey,
		TeamID:    teamID,
		CreatedAt: now,
		UpdatedAt: now,
		Read: ChatReadState{
			IsRead: true,
		},
	}
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_)
		VALUES (?, ?, ?, ?, ?, ?, '', '', '')`,
		chatID, summary.ChatName, agentKey, nilIfEmpty(teamID), now, now)
	if err != nil {
		return Summary{}, false, err
	}
	// Create directory for uploads/attachments
	_ = os.MkdirAll(s.ChatDir(chatID), 0o755)
	return summary, true, nil
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSummary(chatID)
}

func (s *FileStore) UpdateAgentKey(chatID string, agentKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE CHATS SET AGENT_KEY_=?, UPDATED_AT_=? WHERE CHAT_ID_=?", agentKey, time.Now().UnixMilli(), chatID)
	return err
}

func (s *FileStore) SetSourceChannel(chatID string, sourceChannel string) error {
	chatID = strings.TrimSpace(chatID)
	sourceChannel = strings.TrimSpace(sourceChannel)
	if chatID == "" || sourceChannel == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE CHATS SET SOURCE_CHANNEL_=?, UPDATED_AT_=? WHERE CHAT_ID_=?", sourceChannel, time.Now().UnixMilli(), chatID)
	return err
}

func (s *FileStore) SourceChannel(chatID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var sourceChannel string
	err := s.db.QueryRow("SELECT COALESCE(SOURCE_CHANNEL_,'') FROM CHATS WHERE CHAT_ID_=?", chatID).Scan(&sourceChannel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sourceChannel), nil
}

func (s *FileStore) loadSummary(chatID string) (*Summary, error) {
	var sum Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if usage.TotalTokens > 0 {
		sum.Usage = &usage
	}
	applyDerivedReadState(&sum)
	sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
	return &sum, nil
}

func applyDerivedReadState(sum *Summary) {
	if sum == nil {
		return
	}
	sum.Read.IsRead = !RunIDAfter(sum.LastRunID, sum.Read.ReadRunID)
}

func pendingAwaitingFromRow(awaitingID string, runID string, mode string, createdAt int64) *PendingAwaiting {
	if strings.TrimSpace(awaitingID) == "" {
		return nil
	}
	return &PendingAwaiting{
		AwaitingID: awaitingID,
		RunID:      runID,
		Mode:       mode,
		CreatedAt:  createdAt,
	}
}

func (s *FileStore) ListRuns(chatID string) ([]RunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sum, err := s.loadSummary(chatID); err != nil {
		return nil, err
	} else if sum == nil {
		return nil, ErrChatNotFound
	}
	rows, err := s.db.Query(`SELECT RUN_ID_, CHAT_ID_, AGENT_KEY_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
		STARTED_AT_, COMPLETED_AT_,
		USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
		FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
		FROM RUNS WHERE CHAT_ID_=? ORDER BY COMPLETED_AT_ DESC, RUN_ID_ DESC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(
			&item.RunID, &item.ChatID, &item.AgentKey, &item.InitialMessage, &item.AssistantText, &item.FinishReason,
			&item.StartedAt, &item.CompletedAt,
			&item.Usage.PromptTokens, &item.Usage.CompletionTokens, &item.Usage.TotalTokens,
			&item.FeedbackType, &item.FeedbackComment, &item.FeedbackAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_ FROM CHATS WHERE 1=1"
	var args []any
	if agentKey != "" {
		query += " AND AGENT_KEY_=?"
		args = append(args, agentKey)
	}
	query += " ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Summary
	for rows.Next() {
		var sum Summary
		var usage UsageData
		var pendingAwaitingID, pendingRunID, pendingMode string
		var pendingCreatedAt int64
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if usage.TotalTokens > 0 {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if lastRunID != "" && !RunIDAfter(sum.LastRunID, lastRunID) {
			continue
		}
		items = append(items, sum)
	}
	return items, rows.Err()
}
