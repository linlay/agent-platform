package llm

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
)

func (s *llmRunStream) currentContextSize() int {
	return s.lastCallPromptTokens
}

func (s *llmRunStream) estimatedNextCallSize() int {
	if s.lastCallPromptTokens > 0 {
		return s.lastCallPromptTokens + s.lastCallCompletionTokens + s.bytesAfterLastAssistant()/4
	}
	return s.fallbackContextEstimate()
}

// bytesAfterLastAssistant returns bytes of messages strictly after the
// last assistant message. Matches Claude Code's messages.slice(i+1) logic
// in tokenCountWithEstimation: the assistant message itself is covered by
// lastCallCompletionTokens (its output), so only tool_results / new user
// messages added since then count as "new".
func (s *llmRunStream) bytesAfterLastAssistant() int {
	lastAssistant := -1
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	if lastAssistant == -1 {
		return 0
	}
	newBytes := 0
	for i := lastAssistant + 1; i < len(s.messages); i++ {
		raw, _ := json.Marshal(s.messages[i])
		newBytes += len(raw)
	}
	return newBytes
}

func (s *llmRunStream) fallbackContextEstimate() int {
	total := 0
	for _, msg := range s.messages {
		raw, _ := json.Marshal(msg)
		total += len(raw)
	}
	if len(s.toolSpecs) > 0 {
		raw, _ := json.Marshal(s.toolSpecs)
		total += len(raw) / 2
	}
	return total / 4
}

func (s *llmRunStream) effectiveContextWindow() int {
	if s.model.ContextWindow > 0 {
		return s.model.ContextWindow
	}
	return defaultContextWindow
}

func (s *llmRunStream) effectiveReasoningEffort() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.stageSettings.ReasoningEffort)
}

func (s *llmRunStream) currentSystemRef() map[string]any {
	if s == nil {
		return nil
	}
	if !s.systemInitCacheUsed {
		return nil
	}
	cacheKey := strings.TrimSpace(s.systemInitCacheKey)
	if cacheKey == "" {
		cacheKey = SystemInitCacheKey(s.session.Mode, s.promptBuildOptions.Stage)
	}
	snapshot, ok := s.session.SystemInitCache[cacheKey]
	if !ok || strings.TrimSpace(snapshot.Fingerprint) == "" {
		return nil
	}
	if !s.currentSystemMatchesSnapshot(snapshot) {
		return nil
	}
	return map[string]any{
		"cacheKey":    cacheKey,
		"fingerprint": snapshot.Fingerprint,
	}
}

func (s *llmRunStream) currentSystemMatchesSnapshot(snapshot SystemInitSnapshot) bool {
	currentSystem := firstSystemMessageSnapshot(s.messages)
	if !jsonValuesEqual(currentSystem, snapshot.SystemMessage) {
		return false
	}
	return jsonValuesEqual(openAIToolSpecsToAny(s.toolSpecs), snapshot.Tools)
}

func jsonValuesEqual(left any, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftData) == string(rightData)
}

func (s *llmRunStream) resetLastCallUsage() {
	if s == nil {
		return
	}
	s.lastCallPromptTokens = 0
	s.lastCallCompletionTokens = 0
	s.lastCallTotalTokens = 0
	s.lastCallCachedTokens = 0
	s.lastCallReasoningTokens = 0
	s.lastCallPromptCacheHitTokens = 0
	s.lastCallPromptCacheMissTokens = 0
	s.lastCallLLMChatCompletionCount = 0
	s.lastCallToolCallCount = 0
	s.lastCallFirstTokenLatencyMs = 0
	s.lastCallGenerationDurationMs = 0
	s.pendingTimingUsageEmit = false
}

func (s *llmRunStream) emitPendingUsageDelta() {
	s.commitPendingTurnUsage()
	if !s.pendingUsageEmit {
		if s.pendingTimingUsageEmit && s.lastCallHasTiming() {
			s.pending = append(s.pending, s.buildUsageSnapshotDelta())
			s.pendingTimingUsageEmit = false
			currentToolCallCount := s.currentToolCallCountSinceSnapshot()
			s.lastCallToolCallCount = currentToolCallCount
			s.lastSnapshotToolCallCount = s.runToolCallCount
		}
		return
	}
	currentToolCallCount := s.currentToolCallCountSinceSnapshot()
	s.lastCallToolCallCount = currentToolCallCount
	s.lastSnapshotToolCallCount = s.runToolCallCount
	s.pendingUsageEmit = false
}

func (s *llmRunStream) emitDebugLLMChatDelta(trace *llmChatTrace) {
	if s == nil || trace == nil {
		return
	}
	status := trace.statusValue()
	if status == "" {
		status = "unknown"
	}
	s.pending = append(s.pending, DeltaDebugLLMChat{
		ChatID:                          s.session.ChatID,
		ProviderKey:                     s.provider.Key,
		ProviderEndpoint:                trace.payloadString("endpoint"),
		ModelKey:                        s.model.Key,
		ModelID:                         s.model.ModelID,
		ReasoningEffort:                 s.effectiveReasoningEffort(),
		Status:                          status,
		RunSeq:                          trace.runSeqValue(),
		TraceFile:                       trace.relativeFileValue(),
		TraceURL:                        trace.resourceURL(),
		SystemRef:                       s.currentSystemRef(),
		ContextWindow:                   s.effectiveContextWindow(),
		CurrentContextSize:              s.currentContextSize(),
		EstimatedNextCallSize:           s.estimatedNextCallSize(),
		LLMReturnPromptTokens:           s.lastCallPromptTokens,
		LLMReturnCompletionTokens:       s.lastCallCompletionTokens,
		LLMReturnTotalTokens:            s.lastCallTotalTokens,
		LLMReturnCachedTokens:           s.lastCallCachedTokens,
		LLMReturnReasoningTokens:        s.lastCallReasoningTokens,
		LLMReturnPromptCacheHitTokens:   s.lastCallPromptCacheHitTokens,
		LLMReturnPromptCacheMissTokens:  s.lastCallPromptCacheMissTokens,
		LLMReturnLLMChatCompletionCount: s.lastCallLLMChatCompletionCount,
		LLMReturnToolCallCount:          s.lastCallToolCallCount,
		LLMReturnFirstTokenLatencyMs:    s.lastCallFirstTokenLatencyMs,
		LLMReturnGenerationDurationMs:   s.lastCallGenerationDurationMs,
		RunPromptTokens:                 s.runPromptTokens,
		RunCompletionTokens:             s.runCompletionTokens,
		RunTotalTokens:                  s.runTotalTokens,
		RunCachedTokens:                 s.runCachedTokens,
		RunReasoningTokens:              s.runReasoningTokens,
		RunPromptCacheHitTokens:         s.runPromptCacheHitTokens,
		RunPromptCacheMissTokens:        s.runPromptCacheMissTokens,
		RunLLMChatCompletionCount:       s.runLLMChatCompletionCount,
		RunToolCallCount:                s.runToolCallCount,
		RunFirstTokenLatencyTotalMs:     s.runFirstTokenLatencyTotalMs,
		RunFirstTokenLatencyCount:       s.runFirstTokenLatencyCount,
		RunGenerationDurationMs:         s.runGenerationDurationMs,
	})
}

func (s *llmRunStream) accumulateUsage(usage *openAIUsage) {
	if !hasProviderUsage(usage) {
		return
	}
	if s.currentTurn != nil {
		s.currentTurn.usage = cloneOpenAIUsage(usage)
		return
	}
	s.commitUsage(usage)
}

func (s *llmRunStream) commitPendingTurnUsage() {
	if s.currentTurn == nil || s.currentTurn.usage == nil || s.currentTurn.usageCommitted {
		return
	}
	s.currentTurn.usageCommitted = true
	s.commitUsage(s.currentTurn.usage)
}

func (s *llmRunStream) commitUsage(usage *openAIUsage) {
	normalized := normalizeOpenAIUsage(usage, s.protocolConfig)
	s.lastCallPromptTokens = usage.PromptTokens
	s.lastCallCompletionTokens = usage.CompletionTokens
	s.lastCallTotalTokens = usage.TotalTokens
	s.lastCallCachedTokens = normalized.CacheHitTokens
	s.lastCallReasoningTokens = normalized.ReasoningTokens
	s.lastCallPromptCacheHitTokens = normalized.CacheHitTokens
	s.lastCallPromptCacheMissTokens = normalized.CacheMissTokens
	s.runPromptTokens += usage.PromptTokens
	s.runCompletionTokens += usage.CompletionTokens
	s.runTotalTokens += usage.TotalTokens
	s.runCachedTokens += normalized.CacheHitTokens
	s.runReasoningTokens += normalized.ReasoningTokens
	s.runPromptCacheHitTokens += normalized.CacheHitTokens
	s.runPromptCacheMissTokens += normalized.CacheMissTokens
	s.pendingUsageEmit = true
	s.pendingTimingUsageEmit = false
	s.pending = append(s.pending, s.buildUsageSnapshotDelta())
	if s.engine != nil && s.engine.llmConsoleEnabled(llmConsoleUsage) {
		log.Printf("[llm][run:%s][usage] last-call: prompt=%d completion=%d total=%d | run-cumulative: prompt=%d completion=%d total=%d",
			s.session.RunID, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, s.runPromptTokens, s.runCompletionTokens, s.runTotalTokens)
	}
}

func (s *llmRunStream) buildUsageSnapshotDelta() DeltaUsageSnapshot {
	return DeltaUsageSnapshot{
		ChatID:                          s.session.ChatID,
		ModelKey:                        s.model.Key,
		ReasoningEffort:                 s.effectiveReasoningEffort(),
		ContextWindow:                   s.effectiveContextWindow(),
		CurrentContextSize:              s.currentContextSize(),
		EstimatedNextCallSize:           s.estimatedNextCallSize(),
		LLMReturnPromptTokens:           s.lastCallPromptTokens,
		LLMReturnCompletionTokens:       s.lastCallCompletionTokens,
		LLMReturnTotalTokens:            s.lastCallTotalTokens,
		LLMReturnCachedTokens:           s.lastCallCachedTokens,
		LLMReturnReasoningTokens:        s.lastCallReasoningTokens,
		LLMReturnPromptCacheHitTokens:   s.lastCallPromptCacheHitTokens,
		LLMReturnPromptCacheMissTokens:  s.lastCallPromptCacheMissTokens,
		LLMReturnLLMChatCompletionCount: s.lastCallLLMChatCompletionCount,
		LLMReturnToolCallCount:          s.currentToolCallCountSinceSnapshot(),
		LLMReturnFirstTokenLatencyMs:    s.lastCallFirstTokenLatencyMs,
		LLMReturnGenerationDurationMs:   s.lastCallGenerationDurationMs,
		RunPromptTokens:                 s.runPromptTokens,
		RunCompletionTokens:             s.runCompletionTokens,
		RunTotalTokens:                  s.runTotalTokens,
		RunCachedTokens:                 s.runCachedTokens,
		RunReasoningTokens:              s.runReasoningTokens,
		RunPromptCacheHitTokens:         s.runPromptCacheHitTokens,
		RunPromptCacheMissTokens:        s.runPromptCacheMissTokens,
		RunLLMChatCompletionCount:       s.runLLMChatCompletionCount,
		RunToolCallCount:                s.runToolCallCount,
		RunFirstTokenLatencyTotalMs:     s.runFirstTokenLatencyTotalMs,
		RunFirstTokenLatencyCount:       s.runFirstTokenLatencyCount,
		RunGenerationDurationMs:         s.runGenerationDurationMs,
	}
}

func (s *llmRunStream) lastCallHasTiming() bool {
	if s == nil {
		return false
	}
	return s.lastCallFirstTokenLatencyMs > 0 || s.lastCallGenerationDurationMs > 0
}

func (s *llmRunStream) currentToolCallCountSinceSnapshot() int {
	if s == nil {
		return 0
	}
	current := s.runToolCallCount - s.lastSnapshotToolCallCount
	if current < 0 {
		return 0
	}
	return current
}

func (s *llmRunStream) markFirstVisibleDelta() {
	if s == nil || s.currentTurn == nil || !s.currentTurn.firstVisibleAt.IsZero() {
		return
	}
	s.currentTurn.firstVisibleAt = time.Now()
}

func (s *llmRunStream) recordCurrentTurnTiming(completedAt time.Time) {
	if s == nil || s.currentTurn == nil {
		return
	}
	turn := s.currentTurn
	if turn.requestSentAt.IsZero() || turn.firstVisibleAt.IsZero() {
		return
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	firstTokenLatencyMs := nonNegativeDurationMs(turn.requestSentAt, turn.firstVisibleAt)
	generationDurationMs := nonNegativeDurationMs(turn.firstVisibleAt, completedAt)
	s.lastCallFirstTokenLatencyMs = firstTokenLatencyMs
	s.lastCallGenerationDurationMs = generationDurationMs
	s.runFirstTokenLatencyTotalMs += firstTokenLatencyMs
	s.runFirstTokenLatencyCount++
	s.runGenerationDurationMs += generationDurationMs
	s.pendingTimingUsageEmit = true
}

func nonNegativeDurationMs(start time.Time, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

type normalizedOpenAIUsageDetails struct {
	CacheHitTokens  int
	CacheMissTokens int
	ReasoningTokens int
}

func normalizeOpenAIUsage(usage *openAIUsage, protocolConfig protocolRuntimeConfig) normalizedOpenAIUsageDetails {
	if usage == nil {
		return normalizedOpenAIUsageDetails{}
	}
	raw := usage.Raw
	if raw == nil {
		raw = openAIUsageRawMap(usage)
	}
	promptCacheHitTokens := usageCompatInt(raw, protocolConfig, "promptTokensDetails", "cacheHitTokens")
	if promptCacheHitTokens <= 0 {
		promptCacheHitTokens = firstPositiveUsageInt(usage.PromptCacheHitTokens, usage.PromptTokensDetails.CachedTokens)
	}
	promptCacheMissTokens := usageCompatInt(raw, protocolConfig, "promptTokensDetails", "cacheMissTokens")
	if promptCacheMissTokens <= 0 {
		promptCacheMissTokens = usage.PromptCacheMissTokens
	}
	if promptCacheMissTokens <= 0 && promptCacheHitTokens > 0 && usage.PromptTokens > promptCacheHitTokens {
		promptCacheMissTokens = usage.PromptTokens - promptCacheHitTokens
	}
	reasoningTokens := usageCompatInt(raw, protocolConfig, "completionTokensDetails", "reasoningTokens")
	if reasoningTokens <= 0 {
		reasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}

	return normalizedOpenAIUsageDetails{
		CacheHitTokens:  promptCacheHitTokens,
		CacheMissTokens: promptCacheMissTokens,
		ReasoningTokens: reasoningTokens,
	}
}

func usageCompatInt(raw map[string]any, protocolConfig protocolRuntimeConfig, detailKey string, valueKey string) int {
	rule := usageCompatRule(protocolConfig, detailKey, valueKey)
	path := strings.TrimSpace(AnyStringNode(rule["path"]))
	if path == "" {
		return 0
	}
	return intAtPath(raw, path)
}

func usageCompatDerive(protocolConfig protocolRuntimeConfig, detailKey string, valueKey string) string {
	rule := usageCompatRule(protocolConfig, detailKey, valueKey)
	return strings.TrimSpace(AnyStringNode(rule["derive"]))
}

func usageCompatRule(protocolConfig protocolRuntimeConfig, detailKey string, valueKey string) map[string]any {
	usageCompat := responseUsageCompat(protocolConfig)
	details := AnyMapNode(usageCompat[detailKey])
	return AnyMapNode(details[valueKey])
}

func intAtPath(values map[string]any, path string) int {
	var current any = values
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0
		}
		currentMap, _ := current.(map[string]any)
		if currentMap == nil {
			return 0
		}
		current = currentMap[part]
	}
	return AnyIntNode(current)
}

func openAIUsageRawMap(usage *openAIUsage) map[string]any {
	if usage == nil {
		return nil
	}
	raw := map[string]any{
		"prompt_tokens":             usage.PromptTokens,
		"completion_tokens":         usage.CompletionTokens,
		"total_tokens":              usage.TotalTokens,
		"prompt_cache_hit_tokens":   usage.PromptCacheHitTokens,
		"prompt_cache_miss_tokens":  usage.PromptCacheMissTokens,
		"prompt_tokens_details":     map[string]any{"cached_tokens": usage.PromptTokensDetails.CachedTokens},
		"completion_tokens_details": map[string]any{"reasoning_tokens": usage.CompletionTokensDetails.ReasoningTokens},
	}
	return raw
}

func firstPositiveUsageInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func hasProviderUsage(usage *openAIUsage) bool {
	if usage == nil {
		return false
	}
	return usage.PromptTokens > 0 ||
		usage.CompletionTokens > 0 ||
		usage.TotalTokens > 0 ||
		usage.PromptTokensDetails.CachedTokens > 0 ||
		usage.CompletionTokensDetails.ReasoningTokens > 0 ||
		usage.PromptCacheHitTokens > 0 ||
		usage.PromptCacheMissTokens > 0
}

func cloneOpenAIUsage(usage *openAIUsage) *openAIUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

func (s *llmRunStream) drainUsageChunk() {
	if s.currentTurn == nil || s.currentTurn.reader == nil {
		return
	}
	for i := 0; i < 3; i++ {
		_, rawChunk, err := s.readCurrentSSEFrame()
		if err != nil {
			break
		}
		if rawChunk == "" || rawChunk == "[DONE]" {
			break
		}
		var decoded openAIStreamResponse
		if json.Unmarshal([]byte(rawChunk), &decoded) == nil && decoded.Usage != nil {
			s.accumulateUsage(decoded.Usage)
			break
		}
	}
}
