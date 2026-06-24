package server

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/ws"
)

func (s *Server) handleMemoryContextPreview(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	var req api.MemoryContextPreviewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID := strings.TrimSpace(req.ChatID)
	message := strings.TrimSpace(req.Message)
	if chatID == "" || message == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId and message are required"))
		return
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if errors.Is(err, chat.ErrChatNotFound) || summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	agentKey := strings.TrimSpace(summary.AgentKey)
	teamID := strings.TrimSpace(summary.TeamID)
	if agentKey == "" && teamID != "" && s.deps.Registry != nil {
		if team, ok := s.deps.Registry.TeamDefinition(teamID); ok {
			agentKey = strings.TrimSpace(team.DefaultAgentKey)
		}
	}
	if agentKey == "" && s.deps.Registry != nil {
		agentKey = strings.TrimSpace(s.deps.Registry.DefaultAgentKey())
	}
	if agentKey == "" || s.deps.Registry == nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agent not found"))
		return
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agent not found"))
		return
	}

	response := api.MemoryContextPreviewResponse{
		Message:  message,
		AgentKey: agentKey,
		ChatID:   chatID,
		TeamID:   teamID,
		Enabled:  s.memoryEnabledForAgent(agentDef),
		Layers:   []api.MemoryContextPreviewLayer{},
	}
	if !response.Enabled {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}

	topN := s.deps.Config.Memory.ContextTopN
	if topN <= 0 {
		topN = 5
	}
	maxChars := s.deps.Config.Memory.ContextMaxChars
	if maxChars <= 0 {
		maxChars = 4000
	}
	userKey := scopeUserKey(r)
	bundle, err := s.deps.Memory.BuildContextBundle(memory.ContextRequest{
		AgentKey:     agentKey,
		TeamID:       teamID,
		ChatID:       chatID,
		UserKey:      userKey,
		Query:        message,
		TopFacts:     topN,
		TopObs:       topN,
		MaxChars:     maxChars,
		FreezeStable: true,
		PreviewOnly:  true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response.Summary = memoryContextPreviewSummary(bundle)
	response.Prompts = api.MemoryContextPreviewPrompts{
		Stable:      strings.TrimSpace(bundle.StablePrompt),
		Session:     strings.TrimSpace(bundle.SessionPrompt),
		Observation: strings.TrimSpace(bundle.ObservationPrompt),
	}
	response.Layers = memoryContextPreviewLayers(bundle)
	response.Contexts = s.memoryContextPreviewContexts(r.Context(), req, summary, agentDef, bundle)
	response.Decisions = memoryContextPreviewDecisions(bundle)
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) wsMemoryContextPreview(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	payload, err := ws.DecodePayload[api.MemoryContextPreviewRequest](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	chatID := strings.TrimSpace(payload.ChatID)
	message := strings.TrimSpace(payload.Message)
	if chatID == "" || message == "" {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "chatId and message are required")
		return
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if errors.Is(err, chat.ErrChatNotFound) || summary == nil {
		sendMemoryWSError(conn, req, http.StatusNotFound, "not_found", "chat not found")
		return
	}
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	agentKey := strings.TrimSpace(summary.AgentKey)
	teamID := strings.TrimSpace(summary.TeamID)
	if agentKey == "" && teamID != "" && s.deps.Registry != nil {
		if team, ok := s.deps.Registry.TeamDefinition(teamID); ok {
			agentKey = strings.TrimSpace(team.DefaultAgentKey)
		}
	}
	if agentKey == "" && s.deps.Registry != nil {
		agentKey = strings.TrimSpace(s.deps.Registry.DefaultAgentKey())
	}
	if agentKey == "" || s.deps.Registry == nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "agent not found")
		return
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "agent not found")
		return
	}

	response := api.MemoryContextPreviewResponse{
		Message:  message,
		AgentKey: agentKey,
		ChatID:   chatID,
		TeamID:   teamID,
		Enabled:  s.memoryEnabledForAgent(agentDef),
		Layers:   []api.MemoryContextPreviewLayer{},
	}
	if !response.Enabled {
		sendMemoryWSResponse(conn, req, response)
		return
	}

	topN := s.deps.Config.Memory.ContextTopN
	if topN <= 0 {
		topN = 5
	}
	maxChars := s.deps.Config.Memory.ContextMaxChars
	if maxChars <= 0 {
		maxChars = 4000
	}
	bundle, err := s.deps.Memory.BuildContextBundle(memory.ContextRequest{
		AgentKey:     agentKey,
		TeamID:       teamID,
		ChatID:       chatID,
		UserKey:      scopeUserKeyFromContext(ctx, ""),
		Query:        message,
		TopFacts:     topN,
		TopObs:       topN,
		MaxChars:     maxChars,
		FreezeStable: true,
		PreviewOnly:  true,
	})
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.Summary = memoryContextPreviewSummary(bundle)
	response.Prompts = api.MemoryContextPreviewPrompts{
		Stable:      strings.TrimSpace(bundle.StablePrompt),
		Session:     strings.TrimSpace(bundle.SessionPrompt),
		Observation: strings.TrimSpace(bundle.ObservationPrompt),
	}
	response.Layers = memoryContextPreviewLayers(bundle)
	response.Contexts = s.memoryContextPreviewContexts(ctx, payload, summary, agentDef, bundle)
	response.Decisions = memoryContextPreviewDecisions(bundle)
	sendMemoryWSResponse(conn, req, response)
}

func memoryContextPreviewSummary(bundle memory.ContextBundle) api.MemoryContextPreviewSummary {
	return api.MemoryContextPreviewSummary{
		StableCount:      len(bundle.StableFacts),
		SessionCount:     len(bundle.SessionSummaries),
		ObservationCount: len(bundle.RelevantObservations),
		StableChars:      len(strings.TrimSpace(bundle.StablePrompt)),
		SessionChars:     len(strings.TrimSpace(bundle.SessionPrompt)),
		ObservationChars: len(strings.TrimSpace(bundle.ObservationPrompt)),
		DisclosedLayers:  append([]string(nil), bundle.DisclosedLayers...),
		StopReason:       strings.TrimSpace(bundle.StopReason),
		SnapshotID:       strings.TrimSpace(bundle.SnapshotID),
		CandidateCounts:  cloneIntMap(bundle.CandidateCounts),
		SelectedCounts:   cloneIntMap(bundle.SelectedCounts),
	}
}

func memoryContextPreviewLayers(bundle memory.ContextBundle) []api.MemoryContextPreviewLayer {
	layers := []api.MemoryContextPreviewLayer{
		memoryContextPreviewLayer("stable", bundle.StableFacts, bundle.CandidateCounts, bundle.SelectedCounts, len(strings.TrimSpace(bundle.StablePrompt))),
		memoryContextPreviewLayer("session", bundle.SessionSummaries, bundle.CandidateCounts, bundle.SelectedCounts, len(strings.TrimSpace(bundle.SessionPrompt))),
		memoryContextPreviewLayer("observation", bundle.RelevantObservations, bundle.CandidateCounts, bundle.SelectedCounts, len(strings.TrimSpace(bundle.ObservationPrompt))),
	}
	out := make([]api.MemoryContextPreviewLayer, 0, len(layers))
	for _, layer := range layers {
		if layer.CandidateCount == 0 && layer.SelectedCount == 0 && len(layer.Items) == 0 && layer.Chars == 0 {
			continue
		}
		out = append(out, layer)
	}
	return out
}

func (s *Server) memoryContextPreviewContexts(ctx context.Context, req api.MemoryContextPreviewRequest, summary *chat.Summary, agentDef catalog.AgentDefinition, bundle memory.ContextBundle) []api.MemoryContextPreviewContextSection {
	if summary == nil {
		return nil
	}
	message := strings.TrimSpace(req.Message)
	agentKey := strings.TrimSpace(agentDef.Key)
	if agentKey == "" {
		agentKey = strings.TrimSpace(summary.AgentKey)
	}
	teamID := strings.TrimSpace(summary.TeamID)
	principal := PrincipalFromContext(ctx)
	runtimeContext, _ := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey:   agentKey,
		teamID:     teamID,
		role:       defaultRole(""),
		chatID:     summary.ChatID,
		chatName:   summary.ChatName,
		principal:  principal,
		definition: agentDef,
	})
	promptAppend := buildPromptAppendConfig(s.deps.Config.Prompts, agentDef)
	stageSettings := contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask)
	toolNames := buildSessionToolNames(effectiveAgentTools(agentDef), canUseInvokeAgentsTool(agentDef.Mode))
	skillCatalogPrompt := buildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsMarketDir, promptAppend)

	sections := make([]api.MemoryContextPreviewContextSection, 0, 24)
	appendSection := func(promptType string, role string, category string, source string, title string, content string) {
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
		sections = append(sections, api.MemoryContextPreviewContextSection{
			Order:      len(sections) + 1,
			PromptType: promptType,
			Role:       role,
			Category:   category,
			Source:     source,
			Title:      title,
			Content:    content,
			Chars:      len(content),
		})
	}

	appendSection("systemPrompt", "system", "agent.identity", "agent.yml", "Agent Identity", memoryPreviewAgentIdentity(agentDef))
	appendSection("systemPrompt", "system", "agent.soul", "agent/soul prompt", "Soul Prompt", agentDef.SoulPrompt)
	appendSection("systemPrompt", "system", "memory.static", "agent/memory/memory.md", "Static Memory Prompt", agentDef.StaticMemoryPrompt)
	for _, section := range memoryPreviewRuntimeSections(runtimeContext, agentDef, summary.ChatID, "preview", "preview") {
		appendSection("systemPrompt", "system", section.category, section.source, section.title, section.content)
	}
	appendSection("systemPrompt", "system", "memory.stable", "memory.context.stable", "Runtime Context: Stable Memory", bundle.StablePrompt)
	appendSection("systemPrompt", "system", "memory.session", "memory.context.session", "Runtime Context: Current Session", bundle.SessionPrompt)
	appendSection("systemPrompt", "system", "memory.observation", "memory.context.observation", "Runtime Context: Relevant Observations", bundle.ObservationPrompt)
	appendSection("systemPrompt", "system", "stage.instructions", "agent prompt", "Stage Instructions Prompt", agentDef.AgentsPrompt)
	appendSection("systemPrompt", "system", "stage.system", "stage settings", "Stage System Prompt", stageSettings.Execute.SystemPrompt)
	appendSection("systemPrompt", "system", "skill.catalog", "skills", "Skill Catalog Prompt", skillCatalogPrompt)
	appendSection("systemPrompt", "system", "tool.catalog", "toolConfig.tools", "Tool Names", strings.Join(toolNames, "\n"))

	if s.deps.Chats != nil {
		if rawMessages, err := s.deps.Chats.LoadRawMessages(summary.ChatID, chat.DefaultHistoryRunWindow); err == nil {
			for idx, raw := range rawMessages {
				role := strings.TrimSpace(memoryPreviewAnyString(raw["role"]))
				content := strings.TrimSpace(memoryPreviewAnyString(raw["content"]))
				if role == "" || content == "" {
					continue
				}
				appendSection(memoryPreviewPromptTypeForRole(role), role, "history."+role, "raw_messages", "History Message #"+strconv.Itoa(idx+1), content)
			}
		}
	}
	appendSection("userPrompt", "user", "request.message", "preview.message", "Current User Message", message)
	return sections
}

type memoryPreviewRuntimeSection struct {
	category string
	source   string
	title    string
	content  string
}

func memoryPreviewRuntimeSections(context contracts.RuntimeRequestContext, agentDef catalog.AgentDefinition, chatID string, runID string, requestID string) []memoryPreviewRuntimeSection {
	sections := make([]memoryPreviewRuntimeSection, 0, 6)
	hasTag := func(tag string) bool {
		for _, configured := range agentDef.ContextTags {
			if strings.EqualFold(strings.TrimSpace(configured), tag) {
				return true
			}
		}
		return false
	}
	if hasTag("system") {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.system_environment",
			source:   "runtime.context",
			title:    "Runtime Context: System Environment",
			content:  memoryPreviewSystemEnvironment(context),
		})
	}
	if hasTag("session") {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.session",
			source:   "runtime.context",
			title:    "Runtime Context: Session",
			content:  memoryPreviewSessionContext(context, chatID, runID, requestID),
		})
	}
	if hasTag("owner") {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.owner",
			source:   "runtime.context",
			title:    "Runtime Context: Owner",
			content:  memoryPreviewOwnerContext(context.LocalPaths.OwnerDir),
		})
	}
	if hasTag("all-agents") {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.all_agents",
			source:   "runtime.context",
			title:    "Runtime Context: All Agents",
			content:  memoryPreviewAllAgentsContext(context.AgentDigests),
		})
	}
	if hasRuntimeSandbox(agentDef.Runtime) || context.SandboxContext != nil {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.sandbox",
			source:   "runtime.context",
			title:    "Runtime Context: Sandbox",
			content:  memoryPreviewSandboxContext(context.SandboxContext),
		})
	}
	if hasTag("system") || hasRuntimeSandbox(agentDef.Runtime) || context.SandboxContext != nil {
		sections = append(sections, memoryPreviewRuntimeSection{
			category: "runtime.paths",
			source:   "runtime.context",
			title:    "Runtime Context: Paths",
			content:  memoryPreviewPathsContext(context, hasRuntimeSandbox(agentDef.Runtime)),
		})
	}
	return sections
}

func memoryPreviewAgentIdentity(agentDef catalog.AgentDefinition) string {
	lines := []string{"Agent Identity"}
	appendMemoryPreviewKeyValue(&lines, "key", agentDef.Key)
	appendMemoryPreviewKeyValue(&lines, "name", agentDef.Name)
	appendMemoryPreviewKeyValue(&lines, "role", agentDef.Role)
	appendMemoryPreviewKeyValue(&lines, "description", agentDef.Description)
	appendMemoryPreviewKeyValue(&lines, "mode", agentDef.Mode)
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func memoryPreviewSystemEnvironment(context contracts.RuntimeRequestContext) string {
	now := time.Now()
	lines := []string{
		"Runtime Context: System Environment",
		"os: " + runtime.GOOS,
		"arch: " + runtime.GOARCH,
		"timezone: " + now.Location().String(),
		"datetime: " + now.Format(time.RFC3339),
		"language: 中文",
	}
	if context.LocalMode {
		lines = append(lines, "mode: local")
	}
	return strings.Join(lines, "\n")
}

func memoryPreviewSessionContext(context contracts.RuntimeRequestContext, chatID string, runID string, requestID string) string {
	lines := []string{"Runtime Context: Session"}
	appendMemoryPreviewKeyValue(&lines, "chatId", chatID)
	appendMemoryPreviewKeyValue(&lines, "runId", runID)
	appendMemoryPreviewKeyValue(&lines, "requestId", requestID)
	appendMemoryPreviewKeyValue(&lines, "teamId", context.TeamID)
	appendMemoryPreviewKeyValue(&lines, "chatName", context.ChatName)
	if context.AuthIdentity != nil {
		appendMemoryPreviewKeyValue(&lines, "subject", context.AuthIdentity.Subject)
		appendMemoryPreviewKeyValue(&lines, "deviceId", context.AuthIdentity.DeviceID)
		appendMemoryPreviewKeyValue(&lines, "scope", context.AuthIdentity.Scope)
	}
	if len(context.References) > 0 {
		lines = append(lines, "references:")
		for _, ref := range context.References {
			appendMemoryPreviewKeyValue(&lines, "- id", ref.ID)
		}
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func memoryPreviewOwnerContext(ownerDir string) string {
	ownerDir = strings.TrimSpace(ownerDir)
	if ownerDir == "" {
		return ""
	}
	return "owner_dir: " + ownerDir
}

func memoryPreviewAllAgentsContext(digests []contracts.AgentDigest) string {
	if len(digests) == 0 {
		return ""
	}
	lines := []string{"Runtime Context: All Agents"}
	for _, digest := range digests {
		if strings.TrimSpace(digest.Key) == "" {
			continue
		}
		lines = append(lines, "---")
		appendMemoryPreviewKeyValue(&lines, "key", digest.Key)
		appendMemoryPreviewKeyValue(&lines, "name", digest.Name)
		appendMemoryPreviewKeyValue(&lines, "role", digest.Role)
		appendMemoryPreviewKeyValue(&lines, "description", digest.Description)
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func memoryPreviewSandboxContext(context *contracts.SandboxContext) string {
	if context == nil {
		return ""
	}
	lines := []string{"Runtime Context: Sandbox"}
	appendMemoryPreviewKeyValue(&lines, "environmentId", context.EnvironmentID)
	appendMemoryPreviewKeyValue(&lines, "defaultEnvironmentId", context.DefaultEnvironmentID)
	appendMemoryPreviewKeyValue(&lines, "level", context.Level)
	if len(context.ExtraMounts) > 0 {
		lines = append(lines, "sandboxMounts:")
		for _, mount := range context.ExtraMounts {
			if strings.TrimSpace(mount) != "" {
				lines = append(lines, "- "+strings.TrimSpace(mount))
			}
		}
	}
	appendMemoryPreviewKeyValue(&lines, "environment_prompt", context.EnvironmentPrompt)
	return strings.Join(lines, "\n")
}

func memoryPreviewPathsContext(context contracts.RuntimeRequestContext, sandbox bool) string {
	lines := []string{"Runtime Context: Paths"}
	if sandbox || context.SandboxContext != nil {
		appendMemoryPreviewKeyValue(&lines, "workspace_dir", context.SandboxPaths.WorkspaceDir)
		appendMemoryPreviewKeyValue(&lines, "chat_dir", context.SandboxPaths.WorkspaceDir)
		appendMemoryPreviewKeyValue(&lines, "root_dir", context.SandboxPaths.RootDir)
		appendMemoryPreviewKeyValue(&lines, "skills_dir", context.SandboxPaths.SkillsDir)
		appendMemoryPreviewKeyValue(&lines, "agent_dir", context.SandboxPaths.AgentDir)
		appendMemoryPreviewKeyValue(&lines, "owner_dir", context.SandboxPaths.OwnerDir)
		appendMemoryPreviewKeyValue(&lines, "chats_dir", context.SandboxPaths.ChatsDir)
		appendMemoryPreviewKeyValue(&lines, "memory_dir", context.SandboxPaths.MemoryDir)
	} else {
		appendMemoryPreviewKeyValue(&lines, "workspace_dir", context.LocalPaths.WorkspaceDir)
		appendMemoryPreviewKeyValue(&lines, "chat_dir", context.LocalPaths.ChatAttachmentsDir)
		appendMemoryPreviewKeyValue(&lines, "root_dir", context.LocalPaths.RootDir)
		appendMemoryPreviewKeyValue(&lines, "skills_dir", context.LocalPaths.SkillsDir)
		appendMemoryPreviewKeyValue(&lines, "agent_dir", context.LocalPaths.AgentDir)
		appendMemoryPreviewKeyValue(&lines, "owner_dir", context.LocalPaths.OwnerDir)
		appendMemoryPreviewKeyValue(&lines, "chats_dir", context.LocalPaths.ChatsDir)
		appendMemoryPreviewKeyValue(&lines, "memory_dir", context.LocalPaths.MemoryDir)
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func memoryPreviewPromptTypeForRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return "systemPrompt"
	case "user":
		return "userPrompt"
	case "assistant":
		return "assistantPrompt"
	case "tool":
		return "toolPrompt"
	default:
		return "historyPrompt"
	}
}

func appendMemoryPreviewKeyValue(lines *[]string, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*lines = append(*lines, key+": "+value)
}

func memoryPreviewAnyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}

func memoryContextPreviewLayer(layer string, items []api.StoredMemoryResponse, candidateCounts map[string]int, selectedCounts map[string]int, chars int) api.MemoryContextPreviewLayer {
	return api.MemoryContextPreviewLayer{
		Layer:          layer,
		CandidateCount: candidateCounts[layer],
		SelectedCount:  selectedCounts[layer],
		Chars:          chars,
		Items:          memoryContextPreviewItems(items),
	}
}

func memoryContextPreviewItems(items []api.StoredMemoryResponse) []api.MemoryContextPreviewItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.MemoryContextPreviewItem, 0, len(items))
	for idx, item := range items {
		out = append(out, api.MemoryContextPreviewItem{
			ID:             strings.TrimSpace(item.ID),
			Kind:           strings.TrimSpace(item.Kind),
			ScopeType:      strings.TrimSpace(item.ScopeType),
			ScopeKey:       strings.TrimSpace(item.ScopeKey),
			Title:          strings.TrimSpace(item.Title),
			Summary:        strings.TrimSpace(item.Summary),
			Category:       strings.TrimSpace(item.Category),
			Importance:     item.Importance,
			Confidence:     item.Confidence,
			Status:         strings.TrimSpace(item.Status),
			SourceType:     strings.TrimSpace(item.SourceType),
			Tags:           append([]string(nil), item.Tags...),
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
			AccessCount:    item.AccessCount,
			LastAccessedAt: item.LastAccessedAt,
			Order:          idx + 1,
		})
	}
	return out
}

func memoryContextPreviewDecisions(bundle memory.ContextBundle) []api.MemoryContextPreviewDecision {
	if len(bundle.Decisions) == 0 {
		return nil
	}
	out := make([]api.MemoryContextPreviewDecision, 0, len(bundle.Decisions))
	for _, decision := range bundle.Decisions {
		out = append(out, api.MemoryContextPreviewDecision{
			Layer:   string(decision.Layer),
			Reason:  strings.TrimSpace(decision.Reason),
			ItemIDs: append([]string(nil), decision.ItemIDs...),
			Traces:  memoryContextPreviewSelectionTraces(decision.Traces),
		})
	}
	return out
}

func memoryContextPreviewSelectionTraces(traces []memory.ItemSelectionTrace) []api.MemorySelectionTrace {
	if len(traces) == 0 {
		return nil
	}
	out := make([]api.MemorySelectionTrace, 0, len(traces))
	for _, trace := range traces {
		out = append(out, api.MemorySelectionTrace{
			ID:       strings.TrimSpace(trace.ID),
			Layer:    string(trace.Layer),
			Selected: trace.Selected,
			Score:    trace.Score,
			Reason:   strings.TrimSpace(trace.Reason),
			ScoreParts: api.MemorySelectionScoreParts{
				Importance:          trace.ScoreParts.Importance,
				EffectiveImportance: trace.ScoreParts.EffectiveImportance,
				Decay:               trace.ScoreParts.Decay,
				AccessBoost:         trace.ScoreParts.AccessBoost,
				Recency:             trace.ScoreParts.Recency,
				ScopeMatch:          trace.ScoreParts.ScopeMatch,
				QueryMatch:          trace.ScoreParts.QueryMatch,
				VectorScore:         trace.ScoreParts.VectorScore,
				ImportanceNorm:      trace.ScoreParts.ImportanceNorm,
				HybridCombined:      trace.ScoreParts.HybridCombined,
			},
		})
	}
	return out
}

func toMemoryHistoryEvents(events []memory.HistoryEvent) []api.MemoryHistoryEvent {
	if len(events) == 0 {
		return []api.MemoryHistoryEvent{}
	}
	out := make([]api.MemoryHistoryEvent, 0, len(events))
	for _, event := range events {
		out = append(out, api.MemoryHistoryEvent{
			ID:         event.ID,
			Timestamp:  event.Timestamp,
			AgentKey:   event.AgentKey,
			ChatID:     event.ChatID,
			RunID:      event.RunID,
			RequestID:  event.RequestID,
			UserKey:    event.UserKey,
			MemoryID:   event.MemoryID,
			MemoryKind: event.MemoryKind,
			ScopeType:  event.ScopeType,
			ScopeKey:   event.ScopeKey,
			Operation:  event.Operation,
			Source:     event.Source,
			Status:     event.Status,
			Before:     event.Before,
			After:      event.After,
			Delta:      event.Delta,
			Meta:       event.Meta,
		})
	}
	return out
}
