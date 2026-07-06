package chat

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const summarySelectColumns = `CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), COALESCE(SOURCE_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
	USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
	USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
	USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_,
	AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_`

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	return s.EnsureChatWithSource(chatID, agentKey, teamID, firstMessage, "")
}

func (s *FileStore) EnsureChatWithSource(chatID string, agentKey string, teamID string, firstMessage string, source string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source = strings.TrimSpace(source)

	// Check if exists
	var existing Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT "+summarySelectColumns+" FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.TeamID, &existing.Source, &existing.SourceChannel, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunID, &existing.LastRunContent, &existing.Read.ReadRunID, &existing.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if err == nil {
		applyDerivedReadState(&existing)
		if hasUsageData(usage) {
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
		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
		Read: ChatReadState{
			IsRead: true,
		},
	}
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, SOURCE_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '')`,
		chatID, summary.ChatName, agentKey, nilIfEmpty(teamID), source, now, now)
	if err != nil {
		return Summary{}, false, err
	}
	return summary, true, nil
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSummary(chatID)
}

func (s *FileStore) RenameChat(chatID string, chatName string) (Summary, error) {
	chatID = strings.TrimSpace(chatID)
	chatName = strings.TrimSpace(chatName)
	if chatID == "" || chatName == "" {
		return Summary{}, ErrChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("UPDATE CHATS SET CHAT_NAME_=?, UPDATED_AT_=? WHERE CHAT_ID_=?", chatName, time.Now().UnixMilli(), chatID)
	if err != nil {
		return Summary{}, err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return Summary{}, ErrChatNotFound
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, err
	}
	if summary == nil {
		return Summary{}, ErrChatNotFound
	}
	return *summary, nil
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
	err := s.db.QueryRow("SELECT "+summarySelectColumns+" FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hasUsageData(usage) {
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

func hasUsageData(usage UsageData) bool {
	return usage.TotalTokens > 0 || usage.LlmChatCompletionCount > 0 || usage.ToolCallCount > 0 || strings.TrimSpace(usage.EstimatedCostCurrency) != "" ||
		usage.FirstTokenLatencyTotalMs > 0 || usage.FirstTokenLatencyCount > 0 || usage.GenerationDurationMs > 0
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
		USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
		USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
		USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, COALESCE(USAGE_MODEL_KEY_,''),
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
			&item.Usage.PromptTokens, &item.Usage.CompletionTokens, &item.Usage.TotalTokens, &item.Usage.CachedTokens, &item.Usage.ReasoningTokens, &item.Usage.PromptCacheHitTokens, &item.Usage.PromptCacheMissTokens, &item.Usage.LlmChatCompletionCount, &item.Usage.ToolCallCount,
			&item.Usage.FirstTokenLatencyTotalMs, &item.Usage.FirstTokenLatencyCount, &item.Usage.GenerationDurationMs,
			&item.Usage.EstimatedCostCurrency, &item.Usage.EstimatedCostInputHit, &item.Usage.EstimatedCostInputMiss, &item.Usage.EstimatedCostOutput, &item.Usage.EstimatedCostTotal, &item.Usage.ModelKey,
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

	query := "SELECT " + summarySelectColumns + " FROM CHATS WHERE 1=1"
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
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if hasUsageData(usage) {
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

func (s *FileStore) RecentChatsByAgent(agentKey string, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT "+summarySelectColumns+" FROM CHATS WHERE AGENT_KEY_=? ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC LIMIT ?", agentKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Summary, 0, limit)
	for rows.Next() {
		var sum Summary
		var usage UsageData
		var pendingAwaitingID, pendingRunID, pendingMode string
		var pendingCreatedAt int64
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if hasUsageData(usage) {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		items = append(items, sum)
	}
	return items, rows.Err()
}
