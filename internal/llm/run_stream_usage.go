package llm

import (
	"encoding/json"
	"log"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
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

func (s *llmRunStream) currentSystemRef() map[string]any {
	if s == nil {
		return nil
	}
	cacheKey := SystemInitCacheKey(s.session.Mode, s.promptBuildOptions.Stage)
	snapshot, ok := s.session.SystemInitCache[cacheKey]
	if !ok || strings.TrimSpace(snapshot.Fingerprint) == "" {
		return nil
	}
	return map[string]any{
		"cacheKey":    cacheKey,
		"fingerprint": snapshot.Fingerprint,
	}
}

func (s *llmRunStream) emitPendingUsageDelta() {
	if !s.pendingUsageEmit {
		return
	}
	s.pendingUsageEmit = false
	s.pending = append(s.pending, DeltaDebugPostCall{
		ChatID:                    s.session.ChatID,
		ModelKey:                  s.model.Key,
		ContextWindow:             s.effectiveContextWindow(),
		CurrentContextSize:        s.currentContextSize(),
		EstimatedNextCallSize:     s.estimatedNextCallSize(),
		LLMReturnPromptTokens:     s.lastCallPromptTokens,
		LLMReturnCompletionTokens: s.lastCallCompletionTokens,
		LLMReturnTotalTokens:      s.lastCallTotalTokens,
		RunPromptTokens:           s.runPromptTokens,
		RunCompletionTokens:       s.runCompletionTokens,
		RunTotalTokens:            s.runTotalTokens,
	})
}

func (s *llmRunStream) accumulateUsage(prompt, completion, total int) {
	s.lastCallPromptTokens = prompt
	s.lastCallCompletionTokens = completion
	s.lastCallTotalTokens = total
	s.runPromptTokens += prompt
	s.runCompletionTokens += completion
	s.runTotalTokens += total
	s.pendingUsageEmit = true
	log.Printf("[llm][run:%s][usage] last-call: prompt=%d completion=%d total=%d | run-cumulative: prompt=%d completion=%d total=%d",
		s.session.RunID, prompt, completion, total, s.runPromptTokens, s.runCompletionTokens, s.runTotalTokens)
}

func (s *llmRunStream) drainUsageChunk() {
	if s.currentTurn == nil || s.currentTurn.reader == nil {
		return
	}
	for i := 0; i < 3; i++ {
		_, rawChunk, err := readSSEFrame(s.currentTurn.reader)
		if err != nil {
			break
		}
		if rawChunk == "" || rawChunk == "[DONE]" {
			break
		}
		var decoded openAIStreamResponse
		if json.Unmarshal([]byte(rawChunk), &decoded) == nil && decoded.Usage != nil {
			s.accumulateUsage(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens, decoded.Usage.TotalTokens)
			break
		}
	}
}
