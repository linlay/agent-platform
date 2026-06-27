package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	completion.ChatID = strings.TrimSpace(completion.ChatID)
	completion.RunID = strings.TrimSpace(completion.RunID)
	if completion.ChatID == "" || completion.RunID == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	if completion.UpdatedAtMillis <= 0 {
		completion.UpdatedAtMillis = now
	}
	if completion.StartedAtMillis <= 0 {
		if startedAt, ok := ParseRunIDMillis(completion.RunID); ok {
			completion.StartedAtMillis = startedAt
		} else {
			completion.StartedAtMillis = completion.UpdatedAtMillis
		}
	}
	if strings.TrimSpace(completion.FinishReason) == "" {
		completion.FinishReason = "complete"
	}
	assistantText := truncateRunes(completion.AssistantText, 200)
	initialMessage := truncateRunes(completion.InitialMessage, 200)
	agentKey := strings.TrimSpace(completion.AgentKey)
	if agentKey == "" {
		_ = s.db.QueryRow("SELECT AGENT_KEY_ FROM CHATS WHERE CHAT_ID_=?", completion.ChatID).Scan(&agentKey)
	}

	_, err := s.db.Exec(`UPDATE CHATS SET LAST_RUN_ID_=?, LAST_RUN_CONTENT_=?, UPDATED_AT_=?,
		USAGE_PROMPT_TOKENS_=USAGE_PROMPT_TOKENS_+?, USAGE_COMPLETION_TOKENS_=USAGE_COMPLETION_TOKENS_+?, USAGE_TOTAL_TOKENS_=USAGE_TOTAL_TOKENS_+?,
		USAGE_CACHED_TOKENS_=USAGE_CACHED_TOKENS_+?, USAGE_REASONING_TOKENS_=USAGE_REASONING_TOKENS_+?,
		USAGE_PROMPT_CACHE_HIT_TOKENS_=USAGE_PROMPT_CACHE_HIT_TOKENS_+?, USAGE_PROMPT_CACHE_MISS_TOKENS_=USAGE_PROMPT_CACHE_MISS_TOKENS_+?,
		USAGE_ESTIMATED_COST_CURRENCY_=CASE WHEN ? <> '' THEN ? ELSE USAGE_ESTIMATED_COST_CURRENCY_ END,
		USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_=USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_+?,
		USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_=USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_+?,
		USAGE_ESTIMATED_COST_OUTPUT_=USAGE_ESTIMATED_COST_OUTPUT_+?,
		USAGE_ESTIMATED_COST_TOTAL_=USAGE_ESTIMATED_COST_TOTAL_+?,
		USAGE_LLM_CHAT_COMPLETION_COUNT_=USAGE_LLM_CHAT_COMPLETION_COUNT_+?,
		USAGE_TOOL_CALL_COUNT_=USAGE_TOOL_CALL_COUNT_+?,
		USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_=USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_+?,
		USAGE_FIRST_TOKEN_LATENCY_COUNT_=USAGE_FIRST_TOKEN_LATENCY_COUNT_+?,
		USAGE_GENERATION_DURATION_MS_=USAGE_GENERATION_DURATION_MS_+?
		WHERE CHAT_ID_=?`,
		completion.RunID, assistantText, completion.UpdatedAtMillis,
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens,
		completion.Usage.CachedTokens, completion.Usage.ReasoningTokens,
		completion.Usage.PromptCacheHitTokens, completion.Usage.PromptCacheMissTokens,
		completion.Usage.EstimatedCostCurrency, completion.Usage.EstimatedCostCurrency,
		completion.Usage.EstimatedCostInputHit, completion.Usage.EstimatedCostInputMiss,
		completion.Usage.EstimatedCostOutput, completion.Usage.EstimatedCostTotal,
		completion.Usage.LlmChatCompletionCount,
		completion.Usage.ToolCallCount,
		completion.Usage.FirstTokenLatencyTotalMs,
		completion.Usage.FirstTokenLatencyCount,
		completion.Usage.GenerationDurationMs,
		completion.ChatID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO RUNS (
			RUN_ID_, CHAT_ID_, AGENT_KEY_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
			STARTED_AT_, COMPLETED_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
			USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
			USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, USAGE_MODEL_KEY_,
			USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(RUN_ID_) DO UPDATE SET
			CHAT_ID_=excluded.CHAT_ID_,
			AGENT_KEY_=excluded.AGENT_KEY_,
			INITIAL_MESSAGE_=excluded.INITIAL_MESSAGE_,
			ASSISTANT_TEXT_=excluded.ASSISTANT_TEXT_,
			FINISH_REASON_=excluded.FINISH_REASON_,
			STARTED_AT_=excluded.STARTED_AT_,
			COMPLETED_AT_=excluded.COMPLETED_AT_,
			USAGE_PROMPT_TOKENS_=excluded.USAGE_PROMPT_TOKENS_,
			USAGE_COMPLETION_TOKENS_=excluded.USAGE_COMPLETION_TOKENS_,
			USAGE_TOTAL_TOKENS_=excluded.USAGE_TOTAL_TOKENS_,
			USAGE_CACHED_TOKENS_=excluded.USAGE_CACHED_TOKENS_,
			USAGE_REASONING_TOKENS_=excluded.USAGE_REASONING_TOKENS_,
			USAGE_PROMPT_CACHE_HIT_TOKENS_=excluded.USAGE_PROMPT_CACHE_HIT_TOKENS_,
			USAGE_PROMPT_CACHE_MISS_TOKENS_=excluded.USAGE_PROMPT_CACHE_MISS_TOKENS_,
			USAGE_ESTIMATED_COST_CURRENCY_=excluded.USAGE_ESTIMATED_COST_CURRENCY_,
			USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_=excluded.USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_,
			USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_=excluded.USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_,
			USAGE_ESTIMATED_COST_OUTPUT_=excluded.USAGE_ESTIMATED_COST_OUTPUT_,
			USAGE_ESTIMATED_COST_TOTAL_=excluded.USAGE_ESTIMATED_COST_TOTAL_,
			USAGE_MODEL_KEY_=excluded.USAGE_MODEL_KEY_,
			USAGE_LLM_CHAT_COMPLETION_COUNT_=excluded.USAGE_LLM_CHAT_COMPLETION_COUNT_,
			USAGE_TOOL_CALL_COUNT_=excluded.USAGE_TOOL_CALL_COUNT_,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_=excluded.USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_,
			USAGE_FIRST_TOKEN_LATENCY_COUNT_=excluded.USAGE_FIRST_TOKEN_LATENCY_COUNT_,
			USAGE_GENERATION_DURATION_MS_=excluded.USAGE_GENERATION_DURATION_MS_`,
		completion.RunID, completion.ChatID, agentKey, initialMessage, assistantText, completion.FinishReason,
		completion.StartedAtMillis, completion.UpdatedAtMillis,
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens,
		completion.Usage.CachedTokens, completion.Usage.ReasoningTokens,
		completion.Usage.PromptCacheHitTokens, completion.Usage.PromptCacheMissTokens,
		completion.Usage.EstimatedCostCurrency,
		completion.Usage.EstimatedCostInputHit, completion.Usage.EstimatedCostInputMiss,
		completion.Usage.EstimatedCostOutput, completion.Usage.EstimatedCostTotal,
		completion.Usage.ModelKey,
		completion.Usage.LlmChatCompletionCount, completion.Usage.ToolCallCount,
		completion.Usage.FirstTokenLatencyTotalMs, completion.Usage.FirstTokenLatencyCount, completion.Usage.GenerationDurationMs)
	return err
}

func (s *FileStore) MarkRead(chatID string, runID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, err
	}
	if sum == nil {
		return Summary{}, ErrChatNotFound
	}

	nextReadRunID := strings.TrimSpace(runID)
	if nextReadRunID == "" {
		nextReadRunID = sum.LastRunID
	}
	if sum.LastRunID != "" && RunIDAfter(nextReadRunID, sum.LastRunID) {
		nextReadRunID = sum.LastRunID
	}
	if !RunIDAfter(nextReadRunID, sum.Read.ReadRunID) {
		nextReadRunID = sum.Read.ReadRunID
	}

	now := time.Now().UnixMilli()
	result, err := s.db.Exec("UPDATE CHATS SET READ_RUN_ID_=?, READ_AT_=? WHERE CHAT_ID_=?", nextReadRunID, now, chatID)
	if err != nil {
		return Summary{}, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return Summary{}, ErrChatNotFound
	}
	sum, err = s.loadSummary(chatID)
	if err != nil || sum == nil {
		return Summary{}, ErrChatNotFound
	}
	return *sum, nil
}

func (s *FileStore) MarkAllRead(agentKey string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `SELECT CHAT_ID_, LAST_RUN_ID_, READ_RUN_ID_ FROM CHATS WHERE LAST_RUN_ID_ != ''`
	var args []any
	if strings.TrimSpace(agentKey) != "" {
		query += ` AND AGENT_KEY_=?`
		args = append(args, strings.TrimSpace(agentKey))
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type unreadChat struct {
		chatID    string
		lastRunID string
	}
	var unread []unreadChat
	for rows.Next() {
		var chatID, lastRunID, readRunID string
		if err := rows.Scan(&chatID, &lastRunID, &readRunID); err != nil {
			return 0, err
		}
		if RunIDAfter(lastRunID, readRunID) {
			unread = append(unread, unreadChat{chatID: chatID, lastRunID: lastRunID})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(unread) == 0 {
		return 0, nil
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	updated := 0
	for _, item := range unread {
		result, execErr := tx.Exec("UPDATE CHATS SET READ_RUN_ID_=?, READ_AT_=? WHERE CHAT_ID_=?", item.lastRunID, now, item.chatID)
		if execErr != nil {
			err = execErr
			return 0, err
		}
		n, _ := result.RowsAffected()
		updated += int(n)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (s *FileStore) SetFeedback(chatID, runID, feedbackType, comment string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	setAt := time.Now().UnixMilli()
	if strings.TrimSpace(feedbackType) == "clear" {
		setAt = 0
		feedbackType = ""
		comment = ""
	}
	result, err := s.db.Exec(`UPDATE RUNS
		SET FEEDBACK_TYPE_=?, FEEDBACK_COMMENT_=?, FEEDBACK_AT_=?
		WHERE RUN_ID_=? AND CHAT_ID_=?`,
		strings.TrimSpace(feedbackType), strings.TrimSpace(comment), setAt,
		strings.TrimSpace(runID), strings.TrimSpace(chatID))
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return 0, ErrRunNotFound
	}
	return setAt, nil
}

func (s *FileStore) DeleteChat(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return os.ErrPermission
	}
	result, err := s.db.Exec("DELETE FROM CHATS WHERE CHAT_ID_=?", chatID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrChatNotFound
	}
	if _, err := s.db.Exec("DELETE FROM RUNS WHERE CHAT_ID_=?", chatID); err != nil {
		return err
	}
	if err := os.Remove(s.chatJSONLPath(chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(s.ChatDir(chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileStore) AgentChatStats() (map[string]AgentChatStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT AGENT_KEY_, LAST_RUN_ID_, READ_RUN_ID_ FROM CHATS`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]AgentChatStats{}
	for rows.Next() {
		var agentKey, lastRunID, readRunID string
		if err := rows.Scan(&agentKey, &lastRunID, &readRunID); err != nil {
			return nil, err
		}
		item := stats[agentKey]
		item.TotalCount++
		if lastRunID != "" && RunIDAfter(lastRunID, readRunID) {
			item.UnreadCount++
		}
		stats[agentKey] = item
	}
	return stats, rows.Err()
}

func (s *FileStore) ResolveResource(file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	if IsToolInternalPath(clean) {
		return "", os.ErrPermission
	}
	path := filepath.Join(s.root, clean)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FileStore) ChatDir(chatID string) string {
	return filepath.Join(s.root, chatID)
}
