package chat

import (
	"os"
	"strings"
)

func (s *FileStore) RestoreArchivedChat(archived ArchivedChat) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID := strings.TrimSpace(archived.Summary.ChatID)
	if !ValidChatID(chatID) {
		return Summary{}, os.ErrPermission
	}
	if err := validateArchivedChatTimeContract(archived, "archive.restore"); err != nil {
		return Summary{}, err
	}
	if existing, err := s.loadSummary(chatID); err != nil {
		return Summary{}, err
	} else if existing != nil {
		return Summary{}, ErrChatAlreadyActive
	}
	if err := s.ensureRestorePathAvailable(chatID); err != nil {
		return Summary{}, err
	}

	usage := UsageData{}
	if archived.Summary.Usage != nil {
		usage = *archived.Summary.Usage
	}
	readRunID := strings.TrimSpace(archived.Summary.Read.ReadRunID)
	if archived.Summary.Read.IsRead && readRunID == "" {
		readRunID = archived.Summary.LastRunID
	}
	var readAt any
	if archived.Summary.Read.ReadAt != nil {
		readAt = *archived.Summary.Read.ReadAt
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Summary{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`INSERT INTO CHATS (
			CHAT_ID_, CHAT_NAME_, OWNER_TYPE_, AGENT_KEY_, TEAM_ID_, SOURCE_, SOURCE_CHANNEL_, CREATED_AT_, UPDATED_AT_, LAST_RUN_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
			USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
			USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_,
			USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chatID, archived.Summary.ChatName, normalizedStoredOwnerType(archived.Summary.OwnerType, archived.Summary.AgentKey, archived.Summary.TeamID), archived.Summary.AgentKey, nilIfEmpty(archived.Summary.TeamID), archived.Summary.Source, archived.Summary.SourceChannel,
		archived.Summary.CreatedAt, archived.Summary.UpdatedAt, archived.Summary.LastRunAt, archived.Summary.LastRunID, archived.Summary.LastRunContent, readRunID, readAt,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CachedTokens, usage.ReasoningTokens,
		usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
		usage.EstimatedCostCurrency, usage.EstimatedCostInputHit, usage.EstimatedCostInputMiss, usage.EstimatedCostOutput, usage.EstimatedCostTotal,
		usage.LlmChatCompletionCount, usage.ToolCallCount,
		usage.FirstTokenLatencyTotalMs, usage.FirstTokenLatencyCount, usage.GenerationDurationMs)
	if err != nil {
		return Summary{}, err
	}

	for _, run := range archived.Runs {
		_, err = tx.Exec(`INSERT INTO RUNS (
				RUN_ID_, CHAT_ID_, OWNER_TYPE_, AGENT_KEY_, TEAM_ID_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
				STARTED_AT_, COMPLETED_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
				USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
				USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, USAGE_MODEL_KEY_,
				USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
				USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
				FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.RunID, run.ChatID, normalizedStoredOwnerType(run.OwnerType, run.AgentKey, run.TeamID), run.AgentKey, nilIfEmpty(run.TeamID), run.InitialMessage, run.AssistantText, run.FinishReason,
			run.StartedAt, run.CompletedAt,
			run.Usage.PromptTokens, run.Usage.CompletionTokens, run.Usage.TotalTokens, run.Usage.CachedTokens, run.Usage.ReasoningTokens,
			run.Usage.PromptCacheHitTokens, run.Usage.PromptCacheMissTokens,
			run.Usage.EstimatedCostCurrency, run.Usage.EstimatedCostInputHit, run.Usage.EstimatedCostInputMiss, run.Usage.EstimatedCostOutput, run.Usage.EstimatedCostTotal, run.Usage.ModelKey,
			run.Usage.LlmChatCompletionCount, run.Usage.ToolCallCount,
			run.Usage.FirstTokenLatencyTotalMs, run.Usage.FirstTokenLatencyCount, run.Usage.GenerationDurationMs,
			run.FeedbackType, run.FeedbackComment, run.FeedbackAt)
		if err != nil {
			return Summary{}, err
		}
	}

	wroteJSONL := false
	if strings.TrimSpace(archived.JSONLContent) != "" {
		if err := os.WriteFile(s.chatJSONLPath(chatID), []byte(archived.JSONLContent), 0o644); err != nil {
			return Summary{}, err
		}
		wroteJSONL = true
	}
	if err := tx.Commit(); err != nil {
		if wroteJSONL {
			_ = os.Remove(s.chatJSONLPath(chatID))
		}
		return Summary{}, err
	}
	committed = true

	summary, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, err
	}
	if summary == nil {
		return Summary{}, ErrChatNotFound
	}
	return *summary, nil
}

func (s *FileStore) ensureRestorePathAvailable(chatID string) error {
	if _, err := os.Stat(s.chatJSONLPath(chatID)); err == nil {
		return ErrChatAlreadyActive
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(s.ChatDir(chatID)); err == nil {
		return ErrChatAlreadyActive
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
