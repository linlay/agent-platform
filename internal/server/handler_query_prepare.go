package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type preparedQuery struct {
	Req                api.QueryRequest
	Summary            chat.Summary
	Created            bool
	AgentDef           catalog.AgentDefinition
	TeamSnapshot       *catalog.TeamSnapshot
	Session            contracts.QuerySession
	MemoryUsageSummary *api.MemoryUsageSummary
	SystemInitLine     *chat.QueryLineSystem
	ResourceBaseURL    string
	Release            queryReleaseFunc
	ContinueRun        bool
	InitialSeq         int64
	SyntheticBootstrap *stream.SyntheticQuery
	Execution          *queryExecutionOptions
}

type queryExecutionOptions struct {
	StepLineStore   chat.StepLineStore
	CompletionStore chat.Store
	HiddenRun       bool
	QueryMetadata   map[string]any
	BTWID           string
	ParentChatID    string
}

func (s *Server) resolvedQueryExecution(prepared preparedQuery) queryExecutionOptions {
	if prepared.Execution == nil {
		return queryExecutionOptions{
			StepLineStore:   s.deps.Chats,
			CompletionStore: s.deps.Chats,
		}
	}
	resolved := *prepared.Execution
	if resolved.StepLineStore == nil {
		resolved.StepLineStore = s.deps.Chats
	}
	return resolved
}

type statusError = runtimetypes.RequestError

type queryReleaseFunc = func()

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}

func decodeQueryRequest(r *http.Request) (api.QueryRequest, error) {
	var req api.QueryRequest
	if err := decodeJSON(r, &req); err != nil {
		if errors.Is(err, api.ErrRequiredSkillKeysRemoved) {
			const code = "required_skill_keys_removed"
			return api.QueryRequest{}, &statusError{
				Status:  http.StatusBadRequest,
				Code:    code,
				Message: api.RequiredSkillKeysRemovedMessage,
				Data: map[string]any{
					"error": map[string]any{"code": code, "message": api.RequiredSkillKeysRemovedMessage},
				},
			}
		}
		message := "invalid request body"
		if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			message = api.ReferenceSandboxPathRemovedMessage
		}
		return api.QueryRequest{}, &statusError{Status: http.StatusBadRequest, Message: message}
	}
	return req, nil
}

func chatAgentMode(agentDef catalog.AgentDefinition, orchestratedTeam bool) string {
	if orchestratedTeam {
		return "TEAM"
	}
	return catalog.AgentModeForAPI(agentDef.Mode)
}

func buildMemoryUsageSummary(staticMemoryPrompt string, bundle memory.ContextBundle) *api.MemoryUsageSummary {
	hitItems := buildMemoryHitItems(bundle)
	summary := &api.MemoryUsageSummary{
		HasStaticMemory:  strings.TrimSpace(staticMemoryPrompt) != "",
		StableCount:      len(bundle.StableFacts),
		SessionCount:     len(bundle.SessionSummaries),
		ObservationCount: len(bundle.RelevantObservations),
		StableChars:      len(strings.TrimSpace(bundle.StablePrompt)),
		SessionChars:     len(strings.TrimSpace(bundle.SessionPrompt)),
		ObservationChars: len(strings.TrimSpace(bundle.ObservationPrompt)),
		StableItems:      buildMemoryUsageItems(bundle.StableFacts),
		SessionItems:     buildMemoryUsageItems(bundle.SessionSummaries),
		ObservationItems: buildMemoryUsageItems(bundle.RelevantObservations),
		DisclosedLayers:  append([]string(nil), bundle.DisclosedLayers...),
		SnapshotID:       strings.TrimSpace(bundle.SnapshotID),
		StopReason:       strings.TrimSpace(bundle.StopReason),
		CandidateCounts:  cloneIntMap(bundle.CandidateCounts),
		SelectedCounts:   cloneIntMap(bundle.SelectedCounts),
	}
	summary.UserHint = buildMemoryUserHint(hitItems)
	if !summary.HasStaticMemory && summary.StableCount == 0 && summary.SessionCount == 0 && summary.ObservationCount == 0 {
		return nil
	}
	return summary
}

func buildMemoryUsageItems(items []api.StoredMemoryResponse) []api.MemoryUsageItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.MemoryUsageItem, 0, len(items))
	for _, item := range items {
		out = append(out, api.MemoryUsageItem{
			ID:        strings.TrimSpace(item.ID),
			Kind:      strings.TrimSpace(item.Kind),
			ScopeType: strings.TrimSpace(item.ScopeType),
			Title:     strings.TrimSpace(item.Title),
			Summary:   strings.TrimSpace(item.Summary),
			Category:  strings.TrimSpace(item.Category),
		})
	}
	return out
}

func buildMemoryHitItems(bundle memory.ContextBundle) []api.MemoryHitItem {
	out := make([]api.MemoryHitItem, 0, 3)
	appendHits := func(layer string, items []api.StoredMemoryResponse, limit int) {
		for _, item := range items {
			if limit > 0 && len(out) >= limit {
				return
			}
			out = append(out, api.MemoryHitItem{
				ID:        strings.TrimSpace(item.ID),
				Layer:     strings.TrimSpace(layer),
				Kind:      strings.TrimSpace(item.Kind),
				ScopeType: strings.TrimSpace(item.ScopeType),
				Title:     strings.TrimSpace(item.Title),
				Summary:   strings.TrimSpace(item.Summary),
				Category:  strings.TrimSpace(item.Category),
			})
		}
	}

	appendHits("stable", bundle.StableFacts, 3)
	appendHits("session", bundle.SessionSummaries, 3)
	appendHits("observation", bundle.RelevantObservations, 3)
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildMemoryUserHint(items []api.MemoryHitItem) string {
	if len(items) == 0 {
		return ""
	}
	labels := make([]string, 0, minInt(len(items), 3))
	for idx, item := range items {
		if idx >= 3 {
			break
		}
		label := strings.TrimSpace(item.Title)
		if label == "" {
			label = strings.TrimSpace(item.Summary)
		}
		if label == "" {
			continue
		}
		runes := []rune(label)
		if len(runes) > 24 {
			label = strings.TrimSpace(string(runes[:24])) + "..."
		}
		labels = append(labels, "《"+label+"》")
	}
	if len(labels) == 0 {
		return ""
	}
	return "本次回答借鉴了历史记忆：" + strings.Join(labels, "、")
}

func memoryUsageEventPayload(summary *api.MemoryUsageSummary, chatID string, runID string, agentKey string) map[string]any {
	if summary == nil {
		return nil
	}
	payload := map[string]any{
		"chatId":           strings.TrimSpace(chatID),
		"runId":            strings.TrimSpace(runID),
		"agentKey":         strings.TrimSpace(agentKey),
		"hasStaticMemory":  summary.HasStaticMemory,
		"stableCount":      summary.StableCount,
		"sessionCount":     summary.SessionCount,
		"observationCount": summary.ObservationCount,
		"stableChars":      summary.StableChars,
		"sessionChars":     summary.SessionChars,
		"observationChars": summary.ObservationChars,
	}
	if len(summary.StableItems) > 0 {
		payload["stableItems"] = summary.StableItems
	}
	if len(summary.SessionItems) > 0 {
		payload["sessionItems"] = summary.SessionItems
	}
	if len(summary.ObservationItems) > 0 {
		payload["observationItems"] = summary.ObservationItems
	}
	if strings.TrimSpace(summary.UserHint) != "" {
		payload["userHint"] = strings.TrimSpace(summary.UserHint)
	}
	if len(summary.DisclosedLayers) > 0 {
		payload["disclosedLayers"] = append([]string(nil), summary.DisclosedLayers...)
	}
	if strings.TrimSpace(summary.SnapshotID) != "" {
		payload["snapshotId"] = strings.TrimSpace(summary.SnapshotID)
	}
	if strings.TrimSpace(summary.StopReason) != "" {
		payload["stopReason"] = strings.TrimSpace(summary.StopReason)
	}
	if len(summary.CandidateCounts) > 0 {
		payload["candidateCounts"] = cloneIntMap(summary.CandidateCounts)
	}
	if len(summary.SelectedCounts) > 0 {
		payload["selectedCounts"] = cloneIntMap(summary.SelectedCounts)
	}
	return payload
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

// Reference-only follow-ups accept the same content kinds as the composer.
// Resource existence and selection normalization remain in prepareQueryReferences.
