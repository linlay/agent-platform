package chat

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/deprecation"
)

const summarySelectColumns = `CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(AGENT_MODE_,''), COALESCE(TEAM_ID_,''), COALESCE(SOURCE_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
	USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
	USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
	USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_,
	AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_`

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	return s.EnsureChatWithSource(chatID, agentKey, teamID, firstMessage, "")
}

func (s *FileStore) EnsureChatWithSource(chatID string, agentKey string, teamID string, firstMessage string, source string) (Summary, bool, error) {
	return s.EnsureChatWithSourceAndMode(chatID, agentKey, teamID, firstMessage, source, "")
}

func (s *FileStore) EnsureChatWithSourceAndMode(chatID string, agentKey string, teamID string, firstMessage string, source string, agentMode string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source = strings.TrimSpace(source)

	// Check if exists
	var existing Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT "+summarySelectColumns+" FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.AgentMode, &existing.TeamID, &existing.Source, &existing.SourceChannel, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunAt, &existing.LastRunID, &existing.LastRunContent, &existing.Read.ReadRunID, &existing.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if err == nil {
		applyDerivedReadState(&existing)
		if hasUsageData(usage) {
			existing.Usage = &usage
		}
		existing.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if err := validateActiveSummaryTimeContract(existing, "chat.summary"); err != nil {
			return Summary{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Summary{}, false, err
	}

	now := time.Now().UnixMilli()
	if isTeamOwner(agentKey, teamID) {
		agentKey = ""
	}
	agentMode = normalizeStoredAgentMode(agentMode, agentKey, teamID)
	summary := Summary{
		ChatID:    chatID,
		ChatName:  defaultChatName(firstMessage),
		AgentKey:  agentKey,
		AgentMode: agentMode,
		TeamID:    teamID,
		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
		Read: ChatReadState{
			IsRead: true,
		},
	}
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_MODE_, TEAM_ID_, SOURCE_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '')`,
		chatID, summary.ChatName, agentKey, agentMode, nilIfEmpty(teamID), source, now, now)
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
	if existing, err := s.loadSummary(chatID); err != nil {
		return Summary{}, err
	} else if existing == nil {
		return Summary{}, ErrChatNotFound
	}

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
	return s.UpdateAgentIdentity(chatID, agentKey, "")
}

func (s *FileStore) UpdateAgentIdentity(chatID string, agentKey string, agentMode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return err
	}
	if summary == nil {
		return ErrChatNotFound
	}
	if strings.TrimSpace(summary.TeamID) != "" {
		return deprecation.New("historical Team chat owner cannot be changed; create a new Team chat using teamId only")
	}
	if strings.TrimSpace(agentMode) == "" {
		agentMode = summary.AgentMode
	}
	agentMode = normalizeStoredAgentMode(agentMode, agentKey, summary.TeamID)
	_, err = s.db.Exec("UPDATE CHATS SET AGENT_KEY_=?, AGENT_MODE_=?, UPDATED_AT_=? WHERE CHAT_ID_=?", agentKey, agentMode, time.Now().UnixMilli(), chatID)
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
	if summary, err := s.loadSummary(chatID); err != nil {
		return err
	} else if summary == nil {
		return ErrChatNotFound
	}

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
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
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
	if err := validateActiveSummaryTimeContract(sum, "chat.summary"); err != nil {
		return nil, err
	}
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

func isTeamOwner(agentKey string, teamID string) bool {
	return contracts.IsTeamRunOwner(agentKey, teamID)
}

func normalizeStoredAgentMode(agentMode string, agentKey string, teamID string) string {
	// Stored history is immutable evidence. Current Team runs already provide
	// TEAM explicitly; retired or historical values must not be rewritten.
	return strings.TrimSpace(agentMode)
}

// NormalizeAgentModes accepts only public, canonical mode filters. Historical
// rows keep their raw stored values and are not reinterpreted here.
func NormalizeAgentModes(agentModes []string) []string {
	seen := make(map[string]struct{}, len(agentModes))
	result := make([]string, 0, len(agentModes))
	for _, agentMode := range agentModes {
		normalized := strings.TrimSpace(agentMode)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func (s *FileStore) ListRuns(chatID string) ([]RunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sum, err := s.loadSummary(chatID); err != nil {
		return nil, err
	} else if sum == nil {
		return nil, ErrChatNotFound
	}
	rows, err := s.db.Query(`SELECT RUN_ID_, CHAT_ID_, AGENT_KEY_, COALESCE(AGENT_MODE_,''), COALESCE(TEAM_ID_,''), INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
		STARTED_AT_, COMPLETED_AT_,
		USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
		USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
		USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, COALESCE(USAGE_MODEL_KEY_,''),
		FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
		FROM RUNS WHERE CHAT_ID_=? AND COMPLETED_AT_>0 ORDER BY COMPLETED_AT_ DESC, RUN_ID_ DESC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(
			&item.RunID, &item.ChatID, &item.AgentKey, &item.AgentMode, &item.TeamID, &item.InitialMessage, &item.AssistantText, &item.FinishReason,
			&item.StartedAt, &item.CompletedAt,
			&item.Usage.PromptTokens, &item.Usage.CompletionTokens, &item.Usage.TotalTokens, &item.Usage.CachedTokens, &item.Usage.ReasoningTokens, &item.Usage.PromptCacheHitTokens, &item.Usage.PromptCacheMissTokens, &item.Usage.LlmChatCompletionCount, &item.Usage.ToolCallCount,
			&item.Usage.FirstTokenLatencyTotalMs, &item.Usage.FirstTokenLatencyCount, &item.Usage.GenerationDurationMs,
			&item.Usage.EstimatedCostCurrency, &item.Usage.EstimatedCostInputHit, &item.Usage.EstimatedCostInputMiss, &item.Usage.EstimatedCostOutput, &item.Usage.EstimatedCostTotal, &item.Usage.ModelKey,
			&item.FeedbackType, &item.FeedbackComment, &item.FeedbackAt,
		); err != nil {
			return nil, err
		}
		if err := validateActiveRunTimeContract(item, fmt.Sprintf("chat.runs[%d]", len(items))); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	return s.ListChatsWithAgentModes(lastRunID, agentKey, nil)
}

func (s *FileStore) ListChatsWithAgentModes(lastRunID string, agentKey string, agentModes []string) ([]Summary, error) {
	return s.ListChatsWithAgentModesAndLimit(lastRunID, agentKey, agentModes, 0)
}

// ListChatsWithAgentModesAndLimit applies the established filters and order
// before truncating the result. A non-positive limit means no truncation for
// internal callers; public handlers validate supplied limit values first.
func (s *FileStore) ListChatsWithAgentModesAndLimit(lastRunID string, agentKey string, agentModes []string, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT " + summarySelectColumns + " FROM CHATS WHERE 1=1"
	var args []any
	if agentKey != "" {
		query += " AND AGENT_KEY_=?"
		args = append(args, agentKey)
	}
	if agentModes = NormalizeAgentModes(agentModes); len(agentModes) > 0 {
		placeholders := make([]string, 0, len(agentModes))
		for _, agentMode := range agentModes {
			placeholders = append(placeholders, "?")
			args = append(args, agentMode)
		}
		if agentKey == "" {
			// Teams have a public Team owner instead of an agent mode. They remain
			// visible in the global chat list regardless of a mode query.
			query += " AND ((AGENT_KEY_='' AND COALESCE(TEAM_ID_,'') <> '') OR AGENT_MODE_ IN (" + strings.Join(placeholders, ",") + "))"
		} else {
			query += " AND AGENT_MODE_ IN (" + strings.Join(placeholders, ",") + ")"
		}
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
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if hasUsageData(usage) {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if err := validateActiveSummaryTimeContract(sum, fmt.Sprintf("chat.list[%d]", len(items))); err != nil {
			return nil, err
		}
		if lastRunID != "" && !RunIDAfter(sum.LastRunID, lastRunID) {
			continue
		}
		items = append(items, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
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
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if hasUsageData(usage) {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if err := validateActiveSummaryTimeContract(sum, fmt.Sprintf("chat.recent[%d]", len(items))); err != nil {
			return nil, err
		}
		items = append(items, sum)
	}
	return items, rows.Err()
}

// RecentChatsByTeam returns recent chats for one public orchestrated-Team
// owner. Its ordering intentionally matches RecentChatsByAgent.
func (s *FileStore) RecentChatsByTeam(teamID string, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT "+summarySelectColumns+" FROM CHATS WHERE AGENT_KEY_='' AND TEAM_ID_=? ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC LIMIT ?", teamID, limit)
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
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.TeamID, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if hasUsageData(usage) {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if err := validateActiveSummaryTimeContract(sum, fmt.Sprintf("chat.recent_team[%d]", len(items))); err != nil {
			return nil, err
		}
		items = append(items, sum)
	}
	return items, rows.Err()
}
