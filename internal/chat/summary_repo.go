package chat

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const summaryStoredColumns = `CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(AGENT_MODE_,''), COALESCE(SOURCE_,''), COALESCE(SOURCE_CHANNEL_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
	USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
	USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
	USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_,
	AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_`

// Continuation eligibility belongs to single-Chat reads and Query admission.
// Navigation lists read only stored columns, without a latest-Run lookup.
const summarySelectColumns = summaryStoredColumns + `,
 (COALESCE(AWAITING_ID_, '') = '' AND COALESCE((
   SELECT RUN_ID_ = CHATS.LAST_RUN_ID_ AND COMPLETED_AT_ > 0
     AND LOWER(TRIM(FINISH_REASON_)) IN ('error', 'cancel', 'cancelled', 'canceled', 'interrupted')
   FROM RUNS WHERE RUNS.CHAT_ID_ = CHATS.CHAT_ID_
   ORDER BY STARTED_AT_ DESC, rowid DESC LIMIT 1
 ), 0))`

func (s *FileStore) EnsureChat(chatID string, agentKey string, firstMessage string) (Summary, bool, error) {
	return s.EnsureChatWithSource(chatID, agentKey, firstMessage, "")
}

func (s *FileStore) EnsureChatWithSource(chatID string, agentKey string, firstMessage string, source string) (Summary, bool, error) {
	return s.EnsureChatWithSourceAndMode(chatID, agentKey, firstMessage, source, "")
}

func (s *FileStore) EnsureChatWithSourceAndMode(chatID string, agentKey string, firstMessage string, source string, agentMode string) (Summary, bool, error) {
	return s.EnsureChatWithInitialName(chatID, agentKey, firstMessage, source, agentMode, "")
}

func (s *FileStore) EnsureChatWithInitialName(chatID, agentKey, firstMessage, source, agentMode, initialName string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source = strings.TrimSpace(source)

	// Check if exists
	var existing Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT "+summarySelectColumns+" FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.AgentMode, &existing.Source, &existing.SourceChannel, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunAt, &existing.LastRunID, &existing.LastRunContent, &existing.Read.ReadRunID, &existing.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt, &existing.CanContinue)
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

	agentMode = normalizeStoredAgentMode(agentMode)
	summary := Summary{
		ChatID:    chatID,
		ChatName:  defaultChatName(firstMessage),
		AgentKey:  agentKey,
		AgentMode: agentMode,

		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
		Read: ChatReadState{
			IsRead: true,
		},
	}
	if name := strings.TrimSpace(initialName); name != "" {
		summary.ChatName = name
	}
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_MODE_, SOURCE_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '')`,
		chatID, summary.ChatName, agentKey, agentMode, source, now, now)
	if err != nil {
		return Summary{}, false, err
	}
	return summary, true, nil
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary, err := s.loadSummary(chatID)
	if err == nil && summary != nil {
		summary.Pinned = containsChatID(s.readChatPinnedForListLocked().Order, chatID)
	}
	return summary, err
}

func (s *FileStore) PromotePendingChatName(chatID string, firstMessage string) (Summary, bool, error) {
	chatID = strings.TrimSpace(chatID)
	firstMessage = strings.TrimSpace(firstMessage)
	if chatID == "" {
		return Summary{}, false, ErrChatNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, false, err
	}
	if existing == nil {
		return Summary{}, false, ErrChatNotFound
	}
	if firstMessage == "" || !isPendingChatName(existing.ChatName) || strings.TrimSpace(existing.LastRunID) != "" {
		return *existing, false, nil
	}

	chatName := defaultChatName(firstMessage)
	now := time.Now().UnixMilli()
	result, err := s.db.Exec(`UPDATE CHATS SET CHAT_NAME_=?, UPDATED_AT_=?
		WHERE CHAT_ID_=? AND CHAT_NAME_=? AND LAST_RUN_ID_=''`, chatName, now, chatID, existing.ChatName)
	if err != nil {
		return Summary{}, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Summary{}, false, err
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, false, err
	}
	if summary == nil {
		return Summary{}, false, ErrChatNotFound
	}
	return *summary, updated > 0, nil
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

	if summary.AgentKey != "" && summary.AgentKey != agentKey {
		return fmt.Errorf("agentKey does not match chat owner")
	}

	if strings.TrimSpace(agentMode) == "" {
		agentMode = summary.AgentMode
	}
	agentMode = normalizeStoredAgentMode(agentMode)
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
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt, &sum.CanContinue)
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

func normalizeStoredAgentMode(agentMode string) string {
	// Stored history is immutable evidence. Current Team runs already provide
	// TEAM explicitly; retired or historical values must not be rewritten.
	return strings.TrimSpace(agentMode)
}

const (
	agentModeGeneral     = "GENERAL"
	agentModeLegacyReact = "REACT"
)

// NormalizeAgentModes accepts only public, canonical mode filters. Historical
// rows keep their raw stored values; the only alias is GENERAL, whose rows
// written before the rename are stored as REACT, so either spelling selects
// both.
func NormalizeAgentModes(agentModes []string) []string {
	seen := make(map[string]struct{}, len(agentModes))
	result := make([]string, 0, len(agentModes))
	add := func(mode string) {
		if _, ok := seen[mode]; ok {
			return
		}
		seen[mode] = struct{}{}
		result = append(result, mode)
	}
	for _, agentMode := range agentModes {
		normalized := strings.TrimSpace(agentMode)
		switch normalized {
		case "":
		case agentModeGeneral, agentModeLegacyReact:
			add(agentModeGeneral)
			add(agentModeLegacyReact)
		default:
			add(normalized)
		}
	}
	return result
}

// PublicAgentMode maps a stored mode to its current public spelling without
// touching the stored row. Every other historical value is returned as is.
func PublicAgentMode(stored string) string {
	if strings.TrimSpace(stored) == agentModeLegacyReact {
		return agentModeGeneral
	}
	return stored
}

func (s *FileStore) ListRuns(chatID string) ([]RunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sum, err := s.loadSummary(chatID); err != nil {
		return nil, err
	} else if sum == nil {
		return nil, ErrChatNotFound
	}
	rows, err := s.db.Query(`SELECT RUN_ID_, CHAT_ID_, AGENT_KEY_, COALESCE(AGENT_MODE_,''), INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
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
			&item.RunID, &item.ChatID, &item.AgentKey, &item.AgentMode, &item.InitialMessage, &item.AssistantText, &item.FinishReason,
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
	return s.ListChatsWithOptions(ListOptions{LastRunID: lastRunID, AgentKey: agentKey, AgentModes: agentModes, Limit: limit})
}

func (s *FileStore) ListChatsWithOptions(options ListOptions) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listChatsLocked(options, true)
}

func (s *FileStore) listChatsLocked(options ListOptions, applyOrder bool) ([]Summary, error) {
	order := defaultOrderState()
	if applyOrder {
		order = s.readChatOrderForListLocked()
	}
	return s.listChatsWithPresentationLocked(options, applyOrder, s.readChatPinnedForListLocked(), order)
}

// The caller holds mu so membership, ordering and persisted summaries share one snapshot.
func (s *FileStore) listChatsWithPresentationLocked(options ListOptions, applyOrder bool, pins PinnedState, orderState OrderState) ([]Summary, error) {
	lastRunID, limit := options.LastRunID, options.Limit
	where, args := chatListWhere(options)
	owners := newAgentKeyMatcher(options.AgentKeyFilter)

	pinnedOnly := options.Pinned != nil && *options.Pinned
	unpinnedOnly := options.Pinned != nil && !*options.Pinned
	// Recency already is the published order here, so the first matching rows
	// are the final page. Manual order and the pinned-first global list still
	// need every match before they can be arranged.
	stopAtLimit := limit > 0 && (!applyOrder || unpinnedOnly && orderState.SortMode != SortModeManual)
	switch {
	case pinnedOnly:
		// Pins are a short ID list, so read them by primary key instead of
		// scanning every chat and comparing afterwards.
		if len(pins.Order) == 0 {
			return nil, nil
		}
		where, args = appendChatIDsClause(where, args, pins.Order)
	case stopAtLimit:
		ids, err := s.recentChatIDsLocked(where, args, options, owners, pins, limit)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, nil
		}
		where, args = appendChatIDsClause("", nil, ids)
	}

	rows, err := s.db.Query("SELECT "+summaryStoredColumns+" FROM CHATS WHERE 1=1"+where+chatListRecentOrder, args...)
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
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.AgentMode, &sum.Source, &sum.SourceChannel, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.PromptCacheHitTokens, &usage.PromptCacheMissTokens, &usage.LlmChatCompletionCount, &usage.ToolCallCount, &usage.FirstTokenLatencyTotalMs, &usage.FirstTokenLatencyCount, &usage.GenerationDurationMs, &usage.EstimatedCostCurrency, &usage.EstimatedCostInputHit, &usage.EstimatedCostInputMiss, &usage.EstimatedCostOutput, &usage.EstimatedCostTotal, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
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
		if !publishedInChatList(sum.ChatName, sum.LastRunID, lastRunID) || !owners.matches(sum.AgentKey) {
			continue
		}
		sum.Pinned = containsChatID(pins.Order, sum.ChatID)
		if options.Pinned != nil && sum.Pinned != *options.Pinned {
			continue
		}
		items = append(items, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if applyOrder {
		if orderState.SortMode == SortModeManual {
			items = orderSummaries(items, orderState.Order)
		}
	}
	items = applyChatPins(items, pins, options.Pinned, applyOrder)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

const chatListRecentOrder = " ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC"

// chatListWhere renders the owner and mode conditions as " AND ..." clauses.
func chatListWhere(options ListOptions) (string, []any) {
	agentKey := options.AgentKey
	where := ""
	var args []any

	if agentKey != "" || options.OwnerOnly {
		where += " AND AGENT_KEY_=?"
		args = append(args, agentKey)
	}
	if agentModes := NormalizeAgentModes(options.AgentModes); len(agentModes) > 0 {
		placeholders := make([]string, 0, len(agentModes))
		for _, agentMode := range agentModes {
			placeholders = append(placeholders, "?")
			args = append(args, agentMode)
		}
		where += " AND AGENT_MODE_ IN (" + strings.Join(placeholders, ",") + ")"
	}
	return where, args
}

func appendChatIDsClause(where string, args []any, chatIDs []string) (string, []any) {
	placeholders := make([]string, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		placeholders = append(placeholders, "?")
		args = append(args, chatID)
	}
	return where + " AND CHAT_ID_ IN (" + strings.Join(placeholders, ",") + ")", args
}

// recentChatIDsLocked picks the newest matching chats from the few columns the
// filters need, so the wide summary row and its RUNS lookup are only computed
// for the chats that are returned.
func (s *FileStore) recentChatIDsLocked(where string, args []any, options ListOptions, owners agentKeyMatcher, pins PinnedState, limit int) ([]string, error) {
	rows, err := s.db.Query("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, LAST_RUN_ID_ FROM CHATS WHERE 1=1"+where+chatListRecentOrder, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var chatID, chatName, agentKey, lastRunID string
		if err := rows.Scan(&chatID, &chatName, &agentKey, &lastRunID); err != nil {
			return nil, err
		}
		if !publishedInChatList(chatName, lastRunID, options.LastRunID) || !owners.matches(agentKey) {
			continue
		}
		if options.Pinned != nil && containsChatID(pins.Order, chatID) != *options.Pinned {
			continue
		}
		ids = append(ids, chatID)
		if len(ids) >= limit {
			break
		}
	}
	return ids, rows.Err()
}

func publishedInChatList(chatName string, lastRunID string, afterRunID string) bool {
	// Attachment upload may allocate a Chat before the first accepted query.
	// Keep that shell addressable by chatId, but do not publish it as history.
	if isPendingChatName(chatName) && strings.TrimSpace(lastRunID) == "" {
		return false
	}
	return afterRunID == "" || RunIDAfter(lastRunID, afterRunID)
}

// agentKeyMatcher applies AgentKeyFilter while rows are read. The owner keys
// come from the catalog, so they are matched in memory rather than in SQL.
type agentKeyMatcher struct {
	active  bool
	exclude bool
	keys    map[string]struct{}
}

func newAgentKeyMatcher(filter *AgentKeyFilter) agentKeyMatcher {
	if filter == nil {
		return agentKeyMatcher{}
	}
	matcher := agentKeyMatcher{active: true, exclude: filter.Exclude, keys: make(map[string]struct{}, len(filter.Keys))}
	for _, key := range filter.Keys {
		if key = strings.TrimSpace(key); key != "" {
			matcher.keys[key] = struct{}{}
		}
	}
	return matcher
}

func (m agentKeyMatcher) matches(agentKey string) bool {
	if !m.active {
		return true
	}
	_, listed := m.keys[agentKey]
	return listed != m.exclude
}

func (s *FileStore) RecentChatsByAgent(agentKey string, limit int) ([]Summary, error) {
	return s.RecentChatsByOwner(agentKey, limit, nil)
}

func (s *FileStore) RecentChatsByTeam(limit int) ([]Summary, error) {
	return s.RecentChatsByOwner("", limit, nil)
}

// Owner previews retain recent ordering, with pin filtering before truncation.
func (s *FileStore) RecentChatsByOwner(agentKey string, limit int, pinned *bool) ([]Summary, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listChatsLocked(ListOptions{OwnerOnly: true, AgentKey: agentKey, Limit: limit, Pinned: pinned}, false)
}
