package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DeriveChatRequest struct {
	SourceChatID string
	SourceRunID  string
	ChatID       string
	ChatName     string
}

type DeriveChatResult struct {
	Summary      Summary
	SourceChatID string
	SourceRunID  string
	LastRunID    string
	CopiedRuns   int
}

type deriveRewriteContext struct {
	sourceChatID string
	targetChatID string
	sourceDir    string
	targetDir    string
	runIDs       map[string]string
	requestIDs   map[string]string
}

func (s *FileStore) DeriveChat(request DeriveChatRequest) (DeriveChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sourceChatID := strings.TrimSpace(request.SourceChatID)
	targetChatID := strings.TrimSpace(request.ChatID)
	if !ValidChatID(sourceChatID) || !ValidChatID(targetChatID) {
		return DeriveChatResult{}, os.ErrPermission
	}

	sourceSummary, err := s.loadSummary(sourceChatID)
	if err != nil {
		return DeriveChatResult{}, err
	}
	if sourceSummary == nil {
		return DeriveChatResult{}, ErrChatNotFound
	}
	if sourceSummary.PendingAwaiting != nil {
		return DeriveChatResult{}, ErrChatPendingAwaiting
	}
	sourceRunID := strings.TrimSpace(request.SourceRunID)
	if sourceRunID == "" {
		sourceRunID = strings.TrimSpace(sourceSummary.LastRunID)
	}
	if sourceRunID == "" {
		return DeriveChatResult{}, ErrRunNotFound
	}
	if existing, err := s.loadSummary(targetChatID); err != nil {
		return DeriveChatResult{}, err
	} else if existing != nil {
		return DeriveChatResult{}, ErrChatAlreadyActive
	}
	if err := s.ensureRestorePathAvailable(targetChatID); err != nil {
		return DeriveChatResult{}, err
	}

	sourceRuns, err := s.listRunsLocked(sourceChatID)
	if err != nil {
		return DeriveChatResult{}, err
	}
	sourceRunByID := make(map[string]RunSummary, len(sourceRuns))
	for _, run := range sourceRuns {
		sourceRunByID[strings.TrimSpace(run.RunID)] = run
	}
	targetSourceRun, ok := sourceRunByID[sourceRunID]
	if !ok {
		return DeriveChatResult{}, ErrRunNotFound
	}
	if !runSummaryIsComplete(targetSourceRun) {
		return DeriveChatResult{}, ErrRunIncomplete
	}

	lines, err := readPersistedJSONLines(s.chatJSONLPath(sourceChatID))
	if err != nil {
		return DeriveChatResult{}, err
	}
	includedLines, err := deriveIncludedLines(lines, sourceRunID)
	if err != nil {
		return DeriveChatResult{}, err
	}
	runOrder := deriveRunOrder(includedLines)
	if len(runOrder) == 0 {
		return DeriveChatResult{}, ErrRunNotFound
	}
	runIDs := make(map[string]string, len(runOrder))
	for _, sourceID := range runOrder {
		if _, ok := sourceRunByID[sourceID]; !ok {
			return DeriveChatResult{}, ErrRunNotFound
		}
		nextID, err := s.nextDerivedRunIDLocked(runIDs)
		if err != nil {
			return DeriveChatResult{}, err
		}
		runIDs[sourceID] = nextID
	}
	targetRunID := runIDs[sourceRunID]
	if targetRunID == "" {
		return DeriveChatResult{}, ErrRunNotFound
	}
	requestIDs := deriveRequestIDMap(includedLines, runIDs)
	rewriteCtx := deriveRewriteContext{
		sourceChatID: sourceChatID,
		targetChatID: targetChatID,
		sourceDir:    filepath.Clean(s.ChatDir(sourceChatID)),
		targetDir:    filepath.Clean(s.ChatDir(targetChatID)),
		runIDs:       runIDs,
		requestIDs:   requestIDs,
	}
	rewrittenLines := make([]map[string]any, 0, len(includedLines))
	for _, line := range includedLines {
		if rewritten, ok := rewriteDerivedLine(line, rewriteCtx); ok {
			rewrittenLines = append(rewrittenLines, rewritten)
		}
	}

	now := time.Now().UnixMilli()
	chatName := strings.TrimSpace(request.ChatName)
	if chatName == "" {
		chatName = strings.TrimSpace(sourceSummary.ChatName)
	}
	if chatName == "" {
		chatName = defaultChatName(targetSourceRun.InitialMessage)
	}
	usage := aggregateRunUsage(runOrder, sourceRunByID)
	readRunID := targetRunID
	tx, err := s.db.Begin()
	if err != nil {
		return DeriveChatResult{}, err
	}
	committed := false
	wroteFiles := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			if wroteFiles {
				_ = os.Remove(s.chatJSONLPath(targetChatID))
				_ = os.RemoveAll(s.ChatDir(targetChatID))
			}
		}
	}()

	_, err = tx.Exec(`INSERT INTO CHATS (
			CHAT_ID_, CHAT_NAME_, OWNER_TYPE_, AGENT_KEY_, TEAM_ID_, SOURCE_, CREATED_AT_, UPDATED_AT_, LAST_RUN_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
			USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
			USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_,
			USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetChatID, chatName, normalizedStoredOwnerType(sourceSummary.OwnerType, sourceSummary.AgentKey, sourceSummary.TeamID), sourceSummary.AgentKey, nilIfEmpty(sourceSummary.TeamID), "", now, now, now, targetRunID, targetSourceRun.AssistantText, readRunID, now,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CachedTokens, usage.ReasoningTokens,
		usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
		usage.EstimatedCostCurrency, usage.EstimatedCostInputHit, usage.EstimatedCostInputMiss, usage.EstimatedCostOutput, usage.EstimatedCostTotal,
		usage.LlmChatCompletionCount, usage.ToolCallCount,
		usage.FirstTokenLatencyTotalMs, usage.FirstTokenLatencyCount, usage.GenerationDurationMs)
	if err != nil {
		return DeriveChatResult{}, err
	}
	for _, sourceID := range runOrder {
		sourceRun := sourceRunByID[sourceID]
		mappedRunID := runIDs[sourceID]
		_, err = tx.Exec(`INSERT INTO RUNS (
				RUN_ID_, CHAT_ID_, OWNER_TYPE_, AGENT_KEY_, TEAM_ID_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
				STARTED_AT_, COMPLETED_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_,
				USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_,
				USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, USAGE_MODEL_KEY_,
				USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
				USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mappedRunID, targetChatID, normalizedStoredOwnerType(sourceRun.OwnerType, sourceRun.AgentKey, sourceRun.TeamID), sourceRun.AgentKey, nilIfEmpty(sourceRun.TeamID), sourceRun.InitialMessage, sourceRun.AssistantText, sourceRun.FinishReason,
			now, now,
			sourceRun.Usage.PromptTokens, sourceRun.Usage.CompletionTokens, sourceRun.Usage.TotalTokens, sourceRun.Usage.CachedTokens, sourceRun.Usage.ReasoningTokens,
			sourceRun.Usage.PromptCacheHitTokens, sourceRun.Usage.PromptCacheMissTokens,
			sourceRun.Usage.EstimatedCostCurrency, sourceRun.Usage.EstimatedCostInputHit, sourceRun.Usage.EstimatedCostInputMiss, sourceRun.Usage.EstimatedCostOutput, sourceRun.Usage.EstimatedCostTotal, sourceRun.Usage.ModelKey,
			sourceRun.Usage.LlmChatCompletionCount, sourceRun.Usage.ToolCallCount,
			sourceRun.Usage.FirstTokenLatencyTotalMs, sourceRun.Usage.FirstTokenLatencyCount, sourceRun.Usage.GenerationDurationMs)
		if err != nil {
			return DeriveChatResult{}, err
		}
	}
	if err := writeJSONLines(s.chatJSONLPath(targetChatID), rewrittenLines); err != nil {
		return DeriveChatResult{}, err
	}
	wroteFiles = true
	if err := copyDerivedChatDir(s.ChatDir(sourceChatID), s.ChatDir(targetChatID)); err != nil {
		return DeriveChatResult{}, err
	}
	if err := rewriteDerivedPlanTaskSnapshots(s.ChatDir(targetChatID), rewriteCtx); err != nil {
		return DeriveChatResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeriveChatResult{}, err
	}
	committed = true

	summary, err := s.loadSummary(targetChatID)
	if err != nil {
		return DeriveChatResult{}, err
	}
	if summary == nil {
		return DeriveChatResult{}, ErrChatNotFound
	}
	return DeriveChatResult{
		Summary:      *summary,
		SourceChatID: sourceChatID,
		SourceRunID:  sourceRunID,
		LastRunID:    targetRunID,
		CopiedRuns:   len(runOrder),
	}, nil
}

func (s *FileStore) listRunsLocked(chatID string) ([]RunSummary, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, CHAT_ID_, COALESCE(OWNER_TYPE_,''), AGENT_KEY_, COALESCE(TEAM_ID_,''), INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
		STARTED_AT_, COMPLETED_AT_,
		USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, USAGE_CACHED_TOKENS_, USAGE_REASONING_TOKENS_, USAGE_PROMPT_CACHE_HIT_TOKENS_, USAGE_PROMPT_CACHE_MISS_TOKENS_, USAGE_LLM_CHAT_COMPLETION_COUNT_, USAGE_TOOL_CALL_COUNT_,
		USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_, USAGE_FIRST_TOKEN_LATENCY_COUNT_, USAGE_GENERATION_DURATION_MS_,
		USAGE_ESTIMATED_COST_CURRENCY_, USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_, USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_, USAGE_ESTIMATED_COST_OUTPUT_, USAGE_ESTIMATED_COST_TOTAL_, COALESCE(USAGE_MODEL_KEY_,''),
		FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
		FROM RUNS WHERE CHAT_ID_=? AND COMPLETED_AT_>0 ORDER BY COMPLETED_AT_ ASC, RUN_ID_ ASC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(
			&item.RunID, &item.ChatID, &item.OwnerType, &item.AgentKey, &item.TeamID, &item.InitialMessage, &item.AssistantText, &item.FinishReason,
			&item.StartedAt, &item.CompletedAt,
			&item.Usage.PromptTokens, &item.Usage.CompletionTokens, &item.Usage.TotalTokens, &item.Usage.CachedTokens, &item.Usage.ReasoningTokens, &item.Usage.PromptCacheHitTokens, &item.Usage.PromptCacheMissTokens, &item.Usage.LlmChatCompletionCount, &item.Usage.ToolCallCount,
			&item.Usage.FirstTokenLatencyTotalMs, &item.Usage.FirstTokenLatencyCount, &item.Usage.GenerationDurationMs,
			&item.Usage.EstimatedCostCurrency, &item.Usage.EstimatedCostInputHit, &item.Usage.EstimatedCostInputMiss, &item.Usage.EstimatedCostOutput, &item.Usage.EstimatedCostTotal, &item.Usage.ModelKey,
			&item.FeedbackType, &item.FeedbackComment, &item.FeedbackAt,
		); err != nil {
			return nil, err
		}
		item.OwnerType = normalizedStoredOwnerType(item.OwnerType, item.AgentKey, item.TeamID)
		if err := validateActiveRunTimeContract(item, fmt.Sprintf("chat.derive.runs[%d]", len(items))); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func runSummaryIsComplete(run RunSummary) bool {
	if run.CompletedAt == 0 {
		return false
	}
	reason := strings.ToLower(strings.TrimSpace(run.FinishReason))
	return reason == "" || reason == "complete" || reason == "stop"
}

func deriveIncludedLines(lines []map[string]any, sourceRunID string) ([]map[string]any, error) {
	lastIndex := -1
	for i, line := range lines {
		if strings.TrimSpace(stringValue(line["runId"])) == sourceRunID {
			lastIndex = i
		}
	}
	if lastIndex < 0 {
		return nil, ErrRunNotFound
	}
	out := make([]map[string]any, 0, lastIndex+1)
	for _, line := range lines[:lastIndex+1] {
		out = append(out, cloneMapDeep(line))
	}
	return out, nil
}

func deriveRunOrder(lines []map[string]any) []string {
	seen := map[string]struct{}{}
	var order []string
	for _, line := range lines {
		runID := strings.TrimSpace(stringValue(line["runId"]))
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		order = append(order, runID)
	}
	return order
}

func deriveRequestIDMap(lines []map[string]any, runIDs map[string]string) map[string]string {
	out := make(map[string]string, len(runIDs)*2)
	for sourceRunID, targetRunID := range runIDs {
		out[sourceRunID] = targetRunID
	}
	for _, line := range lines {
		sourceRunID := strings.TrimSpace(stringValue(line["runId"]))
		targetRunID := strings.TrimSpace(runIDs[sourceRunID])
		if sourceRunID == "" || targetRunID == "" {
			continue
		}
		query := anyMap(line["query"])
		requestID := strings.TrimSpace(stringValue(query["requestId"]))
		if requestID != "" {
			out[requestID] = targetRunID
		}
	}
	return out
}

func (s *FileStore) nextDerivedRunIDLocked(assigned map[string]string) (string, error) {
	for offset := int64(0); offset < 10000; offset++ {
		candidate := NewRunID()
		used := false
		for _, value := range assigned {
			if value == candidate {
				used = true
				break
			}
		}
		if used {
			continue
		}
		var existing string
		err := s.db.QueryRow("SELECT RUN_ID_ FROM RUNS WHERE RUN_ID_=?", candidate).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("allocate derived run id: exhausted candidates")
}

func rewriteDerivedLine(line map[string]any, ctx deriveRewriteContext) (map[string]any, bool) {
	rewritten := rewriteDerivedValue(line, "", ctx)
	out, ok := rewritten.(map[string]any)
	return out, ok && len(out) > 0
}

func rewriteDerivedValue(value any, key string, ctx deriveRewriteContext) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = rewriteDerivedValue(childValue, childKey, ctx)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = rewriteDerivedValue(item, key, ctx)
		}
		return out
	case string:
		return rewriteDerivedString(typed, key, ctx)
	default:
		return value
	}
}

func rewriteDerivedString(value string, key string, ctx deriveRewriteContext) string {
	trimmedKey := strings.TrimSpace(key)
	switch trimmedKey {
	case "chatId":
		if strings.TrimSpace(value) == ctx.sourceChatID {
			return ctx.targetChatID
		}
	case "runId", "lastRunId", "continuationRunId":
		if mapped := ctx.runIDs[strings.TrimSpace(value)]; mapped != "" {
			return mapped
		}
	case "requestId":
		if mapped := ctx.requestIDs[strings.TrimSpace(value)]; mapped != "" {
			return mapped
		}
	}
	if shouldRewriteDerivedResourceURL(trimmedKey) {
		value = rewriteDerivedResourceURL(value, ctx.sourceChatID, ctx.targetChatID)
	}
	if shouldRewriteDerivedPath(trimmedKey) {
		value = rewriteDerivedAbsolutePath(value, ctx.sourceDir, ctx.targetDir)
	}
	return value
}

func shouldRewriteDerivedResourceURL(key string) bool {
	switch key {
	case "url", "resourceUrl", "resourceURL":
		return true
	default:
		return false
	}
}

func shouldRewriteDerivedPath(key string) bool {
	switch key {
	case "path", "filePath", "file", "planningFile":
		return true
	default:
		return false
	}
}

func rewriteDerivedResourceURL(value string, sourceChatID string, targetChatID string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.Contains(trimmed, "/api/resource") {
		return value
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || strings.TrimSpace(parsed.Path) != "/api/resource" {
		return value
	}
	query := parsed.Query()
	fileParam := strings.TrimSpace(query.Get("file"))
	if fileParam == "" {
		return value
	}
	slashFile := filepath.ToSlash(fileParam)
	if slashFile == sourceChatID {
		query.Set("file", targetChatID)
	} else if strings.HasPrefix(slashFile, sourceChatID+"/") {
		query.Set("file", targetChatID+strings.TrimPrefix(slashFile, sourceChatID))
	} else {
		return value
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func rewriteDerivedAbsolutePath(value string, sourceDir string, targetDir string) string {
	if strings.TrimSpace(value) == "" || strings.HasPrefix(filepath.ToSlash(value), "/workspace/") || !filepath.IsAbs(value) {
		return value
	}
	cleanValue := filepath.Clean(value)
	cleanSource := filepath.Clean(sourceDir)
	if cleanValue == cleanSource {
		return targetDir
	}
	rel, err := filepath.Rel(cleanSource, cleanValue)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return value
	}
	return filepath.Join(targetDir, rel)
}

func aggregateRunUsage(runOrder []string, runs map[string]RunSummary) UsageData {
	var usage UsageData
	for _, runID := range runOrder {
		run := runs[runID]
		usage.PromptTokens += run.Usage.PromptTokens
		usage.CompletionTokens += run.Usage.CompletionTokens
		usage.TotalTokens += run.Usage.TotalTokens
		usage.CachedTokens += run.Usage.CachedTokens
		usage.ReasoningTokens += run.Usage.ReasoningTokens
		usage.PromptCacheHitTokens += run.Usage.PromptCacheHitTokens
		usage.PromptCacheMissTokens += run.Usage.PromptCacheMissTokens
		if strings.TrimSpace(run.Usage.EstimatedCostCurrency) != "" {
			usage.EstimatedCostCurrency = run.Usage.EstimatedCostCurrency
		}
		usage.EstimatedCostInputHit += run.Usage.EstimatedCostInputHit
		usage.EstimatedCostInputMiss += run.Usage.EstimatedCostInputMiss
		usage.EstimatedCostOutput += run.Usage.EstimatedCostOutput
		usage.EstimatedCostTotal += run.Usage.EstimatedCostTotal
		usage.LlmChatCompletionCount += run.Usage.LlmChatCompletionCount
		usage.ToolCallCount += run.Usage.ToolCallCount
		usage.FirstTokenLatencyTotalMs += run.Usage.FirstTokenLatencyTotalMs
		usage.FirstTokenLatencyCount += run.Usage.FirstTokenLatencyCount
		usage.GenerationDurationMs += run.Usage.GenerationDurationMs
	}
	return usage
}

func writeJSONLines(path string, lines []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			return err
		}
	}
	return nil
}

func copyDerivedChatDir(sourceDir string, targetDir string) error {
	info, err := os.Stat(sourceDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	sourceBase := filepath.Clean(sourceDir)
	targetBase := filepath.Clean(targetDir)
	return filepath.WalkDir(sourceBase, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceBase, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(targetBase, 0o755)
		}
		if filepath.ToSlash(rel) == BTWRootDirName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.ToSlash(rel) == filepath.ToSlash(filepath.Join(ToolRootDirName, ToolStateDirName)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(targetBase, rel)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyDerivedFile(path, targetPath, info.Mode().Perm())
	})
}

func copyDerivedFile(sourcePath string, targetPath string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	src, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func rewriteDerivedPlanTaskSnapshots(chatDir string, ctx deriveRewriteContext) error {
	dir := filepath.Join(chatDir, ToolRootDirName, ToolPlanTasksDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		rewritten, _ := rewriteDerivedValue(payload, "", ctx).(map[string]any)
		if len(rewritten) == 0 {
			continue
		}
		targetName := entry.Name()
		if sourceRunID := strings.TrimSuffix(entry.Name(), "_plan.json"); sourceRunID != entry.Name() {
			if mappedRunID := strings.TrimSpace(ctx.runIDs[sourceRunID]); mappedRunID != "" {
				targetName = mappedRunID + "_plan.json"
			}
		}
		targetPath := filepath.Join(dir, targetName)
		encoded, err := json.MarshalIndent(rewritten, "", "  ")
		if err != nil {
			return err
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(targetPath, encoded, 0o644); err != nil {
			return err
		}
		if targetPath != path {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
