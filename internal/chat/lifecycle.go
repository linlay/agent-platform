package chat

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/timecontract"
)

// OnRunStarted records the authoritative run-manager clock before the first
// stream event is observed.  COMPLETED_AT_=0 is an internal active-row
// sentinel only; public mapping omits completedAt until OnRunCompleted writes
// a real epoch-millisecond value.
func (s *FileStore) OnRunStarted(start RunStart) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	start.ChatID = strings.TrimSpace(start.ChatID)
	start.RunID = strings.TrimSpace(start.RunID)
	if start.ChatID == "" || start.RunID == "" {
		return nil
	}
	if err := timecontract.ValidateEpochMillis(start.StartedAtMillis, "startedAt", "chat.runStart.startedAt"); err != nil {
		return err
	}
	summary, err := s.loadSummary(start.ChatID)
	if err != nil {
		return err
	}
	if summary == nil {
		return ErrChatNotFound
	}

	var capturedStartedAt int64
	err = s.db.QueryRow(`SELECT STARTED_AT_ FROM RUNS WHERE RUN_ID_=?`, start.RunID).Scan(&capturedStartedAt)
	if err == nil {
		if err := timecontract.ValidateEpochMillis(capturedStartedAt, "startedAt", "chat.runs.startedAt"); err != nil {
			return err
		}
		if capturedStartedAt != start.StartedAtMillis {
			return &timecontract.Violation{Field: "startedAt", Location: "chat.runs.startedAt", Reason: "does not match registered run start"}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	teamID := strings.TrimSpace(start.TeamID)
	if teamID == "" {
		teamID = strings.TrimSpace(summary.TeamID)
	}
	agentKey := strings.TrimSpace(start.AgentKey)
	if isTeamOwner(agentKey, teamID) {
		agentKey = ""
	} else if agentKey == "" {
		agentKey = strings.TrimSpace(summary.AgentKey)
	}
	agentMode := normalizeStoredAgentMode(start.AgentMode, agentKey, teamID)
	if agentMode == "" {
		agentMode = normalizeStoredAgentMode(summary.AgentMode, agentKey, teamID)
	}

	_, err = s.db.Exec(`INSERT INTO RUNS (
			RUN_ID_, CHAT_ID_, AGENT_KEY_, AGENT_MODE_, TEAM_ID_, INITIAL_MESSAGE_, STARTED_AT_, COMPLETED_AT_
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		start.RunID, start.ChatID, agentKey, agentMode, nilIfEmpty(teamID), truncateRunes(start.InitialMessage, 200), start.StartedAtMillis)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE CHATS SET AGENT_MODE_=?, UPDATED_AT_=? WHERE CHAT_ID_=?`, agentMode, start.StartedAtMillis, start.ChatID)
	return err
}

// LoadRunStartedAt returns the immutable registration clock persisted for a
// run. Restarted continuations must reuse this exact value rather than create
// a new run-manager clock. A missing or malformed row is a time-contract
// violation: callers must fail rather than repair it with time.Now.
func (s *FileStore) LoadRunStartedAt(chatID string, runID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	if chatID == "" || runID == "" {
		return 0, &timecontract.Violation{
			Field:    "startedAt",
			Location: "chat.runs.startedAt",
			Reason:   "registered run start is required",
		}
	}

	var startedAt int64
	err := s.db.QueryRow(`SELECT STARTED_AT_ FROM RUNS WHERE RUN_ID_=? AND CHAT_ID_=?`, runID, chatID).Scan(&startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, &timecontract.Violation{
			Field:    "startedAt",
			Location: "chat.runs.startedAt",
			Reason:   "registered run start is required",
		}
	}
	if err != nil {
		return 0, err
	}
	if err := timecontract.ValidateEpochMillis(startedAt, "startedAt", "chat.runs.startedAt"); err != nil {
		return 0, err
	}
	return startedAt, nil
}

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	completion.ChatID = strings.TrimSpace(completion.ChatID)
	completion.RunID = strings.TrimSpace(completion.RunID)
	if completion.ChatID == "" || completion.RunID == "" {
		return nil
	}
	if err := validateRunCompletionTimeContract(completion, "chat.runCompletion"); err != nil {
		return err
	}
	if summary, err := s.loadSummary(completion.ChatID); err != nil {
		return err
	} else if summary == nil {
		return ErrChatNotFound
	}
	var capturedStartedAt int64
	var capturedAgentMode string
	err := s.db.QueryRow(`SELECT STARTED_AT_, COALESCE(AGENT_MODE_,'') FROM RUNS WHERE RUN_ID_=?`, completion.RunID).Scan(&capturedStartedAt, &capturedAgentMode)
	if err == nil {
		if err := timecontract.ValidateEpochMillis(capturedStartedAt, "startedAt", "chat.runs.startedAt"); err != nil {
			return err
		}
		if capturedStartedAt != completion.StartedAtMillis {
			return &timecontract.Violation{Field: "startedAt", Location: "chat.runs.startedAt", Reason: "does not match registered run start"}
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		// Completion is not allowed to create a lifecycle row: doing so would
		// turn a completion clock into an invented start record. Every product
		// path must call OnRunStarted immediately after registration.
		return &timecontract.Violation{Field: "startedAt", Location: "chat.runs.startedAt", Reason: "registered run start is required"}
	} else {
		return err
	}
	// A run's lifecycle clock is captured by the run manager at registration
	// time and carried through completion.  Deliberately do not derive it from
	// an opaque run ID, the completion time, or the current clock here: any
	// malformed persisted value must reach the strict public read boundary
	// and fail as a time_contract_violation instead of being silently repaired.
	if strings.TrimSpace(completion.FinishReason) == "" {
		completion.FinishReason = "complete"
	}
	assistantText := truncateRunes(completion.AssistantText, 200)
	initialMessage := truncateRunes(completion.InitialMessage, 200)
	var chatAgentKey, chatAgentMode, chatTeamID string
	_ = s.db.QueryRow("SELECT AGENT_KEY_, COALESCE(AGENT_MODE_,''), COALESCE(TEAM_ID_,'') FROM CHATS WHERE CHAT_ID_=?", completion.ChatID).
		Scan(&chatAgentKey, &chatAgentMode, &chatTeamID)
	teamID := strings.TrimSpace(completion.TeamID)
	if teamID == "" {
		teamID = strings.TrimSpace(chatTeamID)
	}
	agentKey := strings.TrimSpace(completion.AgentKey)
	if isTeamOwner(agentKey, teamID) {
		agentKey = ""
	} else if agentKey == "" {
		agentKey = strings.TrimSpace(chatAgentKey)
	}
	agentMode := normalizeStoredAgentMode(completion.AgentMode, agentKey, teamID)
	if agentMode == "" {
		agentMode = normalizeStoredAgentMode(capturedAgentMode, agentKey, teamID)
	}
	if agentMode == "" {
		agentMode = normalizeStoredAgentMode(chatAgentMode, agentKey, teamID)
	}

	_, err = s.db.Exec(`UPDATE CHATS SET LAST_RUN_ID_=?, LAST_RUN_CONTENT_=?, LAST_RUN_AT_=?, AGENT_MODE_=?, UPDATED_AT_=?,
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
		completion.RunID, assistantText, completion.UpdatedAtMillis, agentMode, completion.UpdatedAtMillis,
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
			RUN_ID_, CHAT_ID_, AGENT_KEY_, AGENT_MODE_, TEAM_ID_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
			STARTED_AT_, COMPLETED_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
			USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
			USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, USAGE_MODEL_KEY_,
			USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(RUN_ID_) DO UPDATE SET
			CHAT_ID_=excluded.CHAT_ID_,
			AGENT_KEY_=excluded.AGENT_KEY_,
			AGENT_MODE_=excluded.AGENT_MODE_,
			TEAM_ID_=excluded.TEAM_ID_,
			INITIAL_MESSAGE_=excluded.INITIAL_MESSAGE_,
			ASSISTANT_TEXT_=excluded.ASSISTANT_TEXT_,
			FINISH_REASON_=excluded.FINISH_REASON_,
		STARTED_AT_=RUNS.STARTED_AT_,
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
		completion.RunID, completion.ChatID, agentKey, agentMode, nilIfEmpty(teamID), initialMessage, assistantText, completion.FinishReason,
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
	for _, item := range unread {
		if _, err := s.loadSummary(item.chatID); err != nil {
			return 0, err
		}
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
	if summary, err := s.loadSummary(chatID); err != nil {
		return 0, err
	} else if summary == nil {
		return 0, ErrRunNotFound
	}
	runs, err := s.listRunsLocked(chatID)
	if err != nil {
		return 0, err
	}
	found := false
	for _, run := range runs {
		if strings.TrimSpace(run.RunID) == strings.TrimSpace(runID) {
			found = true
			break
		}
	}
	if !found {
		return 0, ErrRunNotFound
	}

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

	rows, err := s.db.Query(`SELECT AGENT_KEY_, LAST_RUN_ID_, READ_RUN_ID_ FROM CHATS WHERE NOT (AGENT_KEY_='' AND COALESCE(TEAM_ID_,'') <> '')`)
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
		if RunIDAfter(lastRunID, item.LastRunID) {
			item.LastRunID = lastRunID
		}
		stats[agentKey] = item
	}
	return stats, rows.Err()
}

// TeamChatStats aggregates only orchestrated-Team-owned chats, identified by
// their public owner shape (empty agentKey plus a non-empty teamId).
func (s *FileStore) TeamChatStats() (map[string]AgentChatStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT TEAM_ID_, LAST_RUN_ID_, READ_RUN_ID_ FROM CHATS WHERE AGENT_KEY_='' AND COALESCE(TEAM_ID_,'') <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]AgentChatStats{}
	for rows.Next() {
		var teamID, lastRunID, readRunID string
		if err := rows.Scan(&teamID, &lastRunID, &readRunID); err != nil {
			return nil, err
		}
		item := stats[teamID]
		item.TotalCount++
		if lastRunID != "" && RunIDAfter(lastRunID, readRunID) {
			item.UnreadCount++
		}
		if RunIDAfter(lastRunID, item.LastRunID) {
			item.LastRunID = lastRunID
		}
		stats[teamID] = item
	}
	return stats, rows.Err()
}

func (s *FileStore) ResolveResource(file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	if IsToolInternalPath(clean) || IsBTWInternalPath(clean) {
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
