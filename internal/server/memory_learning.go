package server

import (
	"log"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/observability"
)

func (s *Server) autoLearnIfEnabled(chatID string, runID string, agentKey string, teamID string, principal *Principal, requestID string) {
	if s == nil {
		return
	}
	if !s.deps.Config.Memory.AutoRememberEnabled {
		return
	}
	if s.deps.Memory == nil || s.deps.Chats == nil {
		return
	}
	trace, err := s.deps.Chats.LoadRunTrace(strings.TrimSpace(chatID), strings.TrimSpace(runID))
	if err != nil {
		observability.LogMemoryOperation("auto_learn", map[string]any{
			"source":    "server",
			"status":    "error",
			"chatId":    strings.TrimSpace(chatID),
			"runId":     strings.TrimSpace(runID),
			"agentKey":  strings.TrimSpace(agentKey),
			"requestId": strings.TrimSpace(requestID),
			"error":     err.Error(),
		})
		log.Printf("[memory][auto-learn] load run trace failed (chatId=%s runId=%s): %v", chatID, runID, err)
		return
	}
	userKey := "_local_default"
	if principal != nil && strings.TrimSpace(principal.Subject) != "" {
		userKey = strings.TrimSpace(principal.Subject)
	}
	response, err := s.deps.Memory.Learn(memory.LearnInput{
		Request: api.LearnRequest{
			RequestID: strings.TrimSpace(requestID),
			ChatID:    strings.TrimSpace(chatID),
		},
		Trace:           trace,
		AgentKey:        strings.TrimSpace(agentKey),
		TeamID:          strings.TrimSpace(teamID),
		UserKey:         userKey,
		SkillCandidates: s.deps.SkillCandidates,
	})
	if err != nil {
		observability.LogMemoryOperation("auto_learn", map[string]any{
			"source":    "server",
			"status":    "error",
			"chatId":    strings.TrimSpace(chatID),
			"runId":     strings.TrimSpace(runID),
			"agentKey":  strings.TrimSpace(agentKey),
			"teamId":    strings.TrimSpace(teamID),
			"userKey":   userKey,
			"requestId": strings.TrimSpace(requestID),
			"error":     err.Error(),
		})
		log.Printf("[memory][auto-learn] learn failed (chatId=%s runId=%s): %v", chatID, runID, err)
		return
	}
	observability.LogMemoryOperation("auto_learn", map[string]any{
		"source":           "server",
		"status":           "ok",
		"chatId":           strings.TrimSpace(chatID),
		"runId":            strings.TrimSpace(runID),
		"agentKey":         strings.TrimSpace(agentKey),
		"teamId":           strings.TrimSpace(teamID),
		"userKey":          userKey,
		"requestId":        strings.TrimSpace(requestID),
		"accepted":         response.Accepted,
		"observationCount": response.ObservationCount,
		"resultStatus":     strings.TrimSpace(response.Status),
	})
	log.Printf("[memory][auto-learn] completed (chatId=%s runId=%s accepted=%t observations=%d status=%s)",
		chatID, runID, response.Accepted, response.ObservationCount, response.Status)

	s.applyMemoryFeedback(chatID, agentKey, trace)
}

func (s *Server) applyMemoryFeedback(chatID string, agentKey string, trace chat.RunTrace) {
	sqliteStore, ok := s.deps.Memory.(*memory.SQLiteStore)
	if !ok {
		return
	}
	assistantText := strings.TrimSpace(trace.AssistantText)
	if assistantText == "" {
		return
	}
	bundle, err := sqliteStore.BuildContextBundle(memory.ContextRequest{
		AgentKey: agentKey,
		ChatID:   chatID,
		TopFacts: 10,
		TopObs:   10,
		MaxChars: 8000,
	})
	if err != nil {
		return
	}
	var disclosedIDs []string
	for _, d := range bundle.Decisions {
		disclosedIDs = append(disclosedIDs, d.ItemIDs...)
	}
	if len(disclosedIDs) == 0 {
		return
	}
	allItems := append(append([]api.StoredMemoryResponse(nil), bundle.StableFacts...), bundle.SessionSummaries...)
	allItems = append(allItems, bundle.RelevantObservations...)
	signals := memory.ComputeFeedback(disclosedIDs, assistantText, allItems)
	if len(signals) == 0 {
		return
	}
	if err := sqliteStore.ApplyFeedback(signals); err != nil {
		log.Printf("[memory][feedback] apply failed (chatId=%s): %v", chatID, err)
		return
	}
	referenced := 0
	for _, sig := range signals {
		if sig.Referenced {
			referenced++
		}
	}
	referenceRate := float64(0)
	if len(signals) > 0 {
		referenceRate = float64(referenced) / float64(len(signals))
	}
	observability.LogMemoryOperation("disclosure_feedback", map[string]any{
		"chatId":         chatID,
		"agentKey":       agentKey,
		"disclosedTotal": len(signals),
		"referenced":     referenced,
		"unreferenced":   len(signals) - referenced,
		"referenceRate":  referenceRate,
		"stableCount":    len(bundle.StableFacts),
		"sessionCount":   len(bundle.SessionSummaries),
		"obsCount":       len(bundle.RelevantObservations),
		"layers":         bundle.DisclosedLayers,
		"hybrid":         len(bundle.Decisions) > 0 && containsHybrid(bundle.Decisions),
	})
	log.Printf("[memory][feedback] applied (chatId=%s agentKey=%s total=%d referenced=%d rate=%.2f)",
		chatID, agentKey, len(signals), referenced, referenceRate)
}

func containsHybrid(decisions []memory.DisclosureDecision) bool {
	for _, d := range decisions {
		if d.Reason == "hybrid_score" {
			return true
		}
	}
	return false
}
