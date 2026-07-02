package server

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/memory"
	"agent-platform/internal/stream"
)

type preparedQuery struct {
	req                api.QueryRequest
	summary            chat.Summary
	created            bool
	agentDef           catalog.AgentDefinition
	session            contracts.QuerySession
	memoryUsageSummary *api.MemoryUsageSummary
	systemInitLines    []chat.QueryLineSystemInit
	resourceBaseURL    string
	release            queryReleaseFunc
	continueRun        bool
}

type queryAdmission struct {
	req             api.QueryRequest
	existingSummary *chat.Summary
	agentDef        catalog.AgentDefinition
	resourceBaseURL string
	locale          string
}

type statusError struct {
	status  int
	code    string
	message string
	data    any
}

type queryReleaseFunc func()

func (e *statusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}

func (s *Server) prepareQueryAdmission(r *http.Request, requireMessage bool) (queryAdmission, error) {
	var req api.QueryRequest
	if err := decodeJSON(r, &req); err != nil {
		message := "invalid request body"
		if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			message = api.ReferenceSandboxPathRemovedMessage
		}
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: message}
	}
	locale := requestLocale(r, i18n.DefaultLocale)
	if requireMessage && strings.TrimSpace(req.Message) == "" {
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: "message is required"}
	}
	if role, ok := normalizeQueryRole(req.Role); ok {
		req.Role = role
	} else {
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: api.QueryRoleValidationMessage}
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !ok {
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: "accessLevel must be default, auto_approve, or full_access"}
	}
	req.AccessLevel = accessLevel

	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = newRunID()
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = runID
	}
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		chatID = newChatID()
	}
	var existingSummary *chat.Summary
	if s.deps.Chats != nil {
		existingSummary, _ = s.deps.Chats.Summary(chatID)
	}
	if gateErr := s.awaitingQueryGateError(chatID, existingSummary); gateErr != nil {
		return queryAdmission{}, gateErr
	}
	teamID := strings.TrimSpace(req.TeamID)
	if teamID == "" && existingSummary != nil {
		teamID = existingSummary.TeamID
	}
	agentKey := strings.TrimSpace(req.AgentKey)
	usedGlobalDefault := false
	if agentKey == "" && existingSummary != nil {
		agentKey = existingSummary.AgentKey
	}
	if agentKey == "" && teamID != "" {
		if team, ok := s.deps.Registry.TeamDefinition(teamID); ok && team.DefaultAgentKey != "" {
			agentKey = team.DefaultAgentKey
		}
	}
	if agentKey == "" {
		agentKey = s.deps.Registry.DefaultAgentKey()
		usedGlobalDefault = agentKey != ""
	}
	channelID := channel.ChannelForChatID(chatID)
	if usedGlobalDefault && channelID != "" && s.deps.Channels != nil {
		if channelDefault := s.deps.Channels.DefaultAgent(channelID); channelDefault != "" {
			agentKey = channelDefault
		}
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}
	if isProxyRoutedAgent(agentDef) && proxyRequestHasReservedCWD(req.Params) {
		return queryAdmission{}, &statusError{
			status:  http.StatusBadRequest,
			message: "params.cwd is reserved for proxy-routed agents; configure runtimeConfig.workspaceRoot in agent.yml",
		}
	}
	if statusErr := s.applyProxyRoutingConfig(&agentDef); statusErr != nil {
		return queryAdmission{}, statusErr
	}
	if err := s.validateQueryModelOptions(req.Model, agentDef); err != nil {
		return queryAdmission{}, err
	}
	if channelID != "" && s.deps.Channels != nil && !s.deps.Channels.IsAgentAllowed(channelID, agentKey) {
		return queryAdmission{}, &statusError{
			status:  http.StatusForbidden,
			message: "agent " + `"` + agentKey + `" is not allowed on channel "` + channelID + `"`,
		}
	}

	req.ChatID = chatID
	req.AgentKey = agentKey
	req.RequestID = requestID
	req.RunID = runID
	req.TeamID = teamID

	return queryAdmission{
		req:             req,
		existingSummary: existingSummary,
		agentDef:        agentDef,
		resourceBaseURL: requestBaseURL(r),
		locale:          locale,
	}, nil
}

func (s *Server) completeQueryPreparation(ctx context.Context, admission queryAdmission, release queryReleaseFunc) (preparedQuery, error) {
	req := admission.req
	agentDef := admission.agentDef
	chatID := req.ChatID
	agentKey := req.AgentKey
	summary, created, err := s.deps.Chats.EnsureChat(chatID, agentKey, req.TeamID, req.Message)
	if err != nil {
		return preparedQuery{}, err
	}
	if !created && agentKey != "" && agentKey != summary.AgentKey {
		_ = s.deps.Chats.UpdateAgentKey(chatID, agentKey)
		summary.AgentKey = agentKey
	}
	if created {
		// automation/system role 只影响 chat 内部 request.query 的展示语义，
		// 不影响会话在列表里的可见性。
		s.broadcast("chat.created", map[string]any{
			"chatId":    chatID,
			"chatName":  summary.ChatName,
			"agentKey":  agentKey,
			"timestamp": summary.CreatedAt,
		})
	}
	session, err := s.BuildQuerySession(ctx, req, summary, agentDef, querySessionBuildOptions{
		Created:           created,
		Locale:            admission.locale,
		IncludeHistory:    !created,
		IncludeMemory:     true,
		AllowInvokeAgents: canUseInvokeAgentsTool(agentDef.Mode),
	})
	if err != nil {
		return preparedQuery{}, err
	}
	req.References = session.RuntimeContext.References
	if !isProxyAgentMode(agentDef.Mode) {
		applyQueryModelOptionsToSession(req.Model, &session)
	}
	session.CurrentMessages = s.buildCurrentMessages(req, session)
	if !created {
		s.maybeAutoCompact(ctx, req, agentDef, &session)
	}
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	systemInitLines, err := s.prepareSystemInitCache(req, &session, created)
	if err != nil {
		return preparedQuery{}, err
	}

	return preparedQuery{
		req:                req,
		summary:            summary,
		created:            created,
		agentDef:           agentDef,
		session:            session,
		memoryUsageSummary: session.MemoryUsageSummary,
		systemInitLines:    systemInitLines,
		resourceBaseURL:    admission.resourceBaseURL,
		release:            release,
	}, nil
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

func (s *Server) validateQueryModelOptions(options *api.QueryModelOptions, agentDef catalog.AgentDefinition) error {
	if options == nil {
		return nil
	}
	if isProxyAgentMode(agentDef.Mode) {
		return nil
	}
	modelKey := strings.TrimSpace(options.Key)
	reasoningEffort := strings.TrimSpace(options.ReasoningEffort)
	serviceTier := strings.TrimSpace(options.ServiceTier)
	if modelKey == "" && reasoningEffort == "" && serviceTier == "" {
		return nil
	}
	if modelKey != "" {
		if s.deps.Models == nil {
			return &statusError{status: http.StatusServiceUnavailable, message: "model registry is not configured"}
		}
		if catalog.AgentUsesACPCoderBackend(agentDef) {
			options, err, ok := s.listACPCoderModelOptions(agentDef.Key)
			if ok {
				if err != nil {
					return &statusError{status: http.StatusBadGateway, message: "failed to fetch ACP CODER models: " + err.Error()}
				}
				if !agentcoder.ModelKeyInOptions(modelKey, options) {
					return &statusError{status: http.StatusBadRequest, message: "model " + modelKey + " is not available for ACP CODER"}
				}
			} else if err := s.validateLocalChatModelKey(modelKey, false); err != nil {
				return &statusError{status: http.StatusBadRequest, message: err.Error()}
			}
		} else {
			if err := s.validateLocalChatModelKey(modelKey, true); err != nil {
				return &statusError{status: http.StatusBadRequest, message: err.Error()}
			}
		}
	}
	reasoningEffort, ok := normalizeQueryModelReasoningEffort(reasoningEffort)
	if !ok {
		return &statusError{status: http.StatusBadRequest, message: "model.reasoningEffort must be LOW, MEDIUM, HIGH, XHIGH, or MAX; CODER agents also support NONE"}
	}
	if reasoningEffort == "NONE" && !agentcoder.IsMode(agentDef.Mode) {
		return &statusError{status: http.StatusBadRequest, message: "model.reasoningEffort NONE is only supported for CODER agents"}
	}
	if reasoningEffort != "" && reasoningEffort != "NONE" && catalog.AgentUsesACPCoderBackend(agentDef) {
		acpOptions, err, listed := s.listACPCoderModelOptions(agentDef.Key)
		if listed {
			if err != nil {
				return &statusError{status: http.StatusBadGateway, message: "failed to fetch ACP CODER models: " + err.Error()}
			}
			if !reasoningEffortAllowedForACPModel(reasoningEffort, modelKey, acpOptions) {
				return &statusError{status: http.StatusBadRequest, message: "model.reasoningEffort " + reasoningEffort + " is not available for ACP CODER"}
			}
		}
	}
	if reasoningEffort == "XHIGH" || reasoningEffort == "MAX" {
		if !catalog.AgentUsesACPCoderBackend(agentDef) {
			return &statusError{status: http.StatusBadRequest, message: "model.reasoningEffort " + reasoningEffort + " is only supported for ACP CODER"}
		}
	}
	serviceTier, ok = normalizeQueryModelServiceTier(serviceTier)
	if !ok {
		return &statusError{status: http.StatusBadRequest, message: "model.serviceTier must be a non-empty string"}
	}
	if serviceTier != "" {
		if !catalog.AgentUsesACPCoderBackend(agentDef) {
			return &statusError{status: http.StatusBadRequest, message: "model.serviceTier is only supported for ACP CODER"}
		}
		acpOptions, err, listed := s.listACPCoderModelOptions(agentDef.Key)
		if listed {
			if err != nil {
				return &statusError{status: http.StatusBadGateway, message: "failed to fetch ACP CODER models: " + err.Error()}
			}
			if !serviceTierAllowedForACPModel(serviceTier, modelKey, acpOptions) {
				return &statusError{status: http.StatusBadRequest, message: "model.serviceTier " + serviceTier + " is not available for ACP CODER"}
			}
		}
	}
	return nil
}

func applyQueryModelOptionsToSession(options *api.QueryModelOptions, session *contracts.QuerySession) {
	if options == nil || session == nil {
		return
	}
	modelKey := strings.TrimSpace(options.Key)
	reasoningEffort, ok := normalizeQueryModelReasoningEffort(options.ReasoningEffort)
	if modelKey == "" && (reasoningEffort == "" || !ok) {
		return
	}
	if modelKey != "" {
		session.ModelKey = modelKey
	}
	session.StageSettings = applyQueryModelOptionsToRawStageSettings(session.StageSettings, modelKey, reasoningEffort)
	session.ResolvedStageSettings = applyQueryModelOptionsToResolvedStageSettings(session.ResolvedStageSettings, modelKey, reasoningEffort)
}

func normalizeQueryModelReasoningEffort(value string) (string, bool) {
	return agentcoder.NormalizeReasoningEffort(value)
}

func normalizeQueryModelServiceTier(value string) (string, bool) {
	return agentcoder.NormalizeServiceTier(value)
}

func serviceTierAllowedForACPModel(serviceTier string, modelKey string, options []api.CoderModelOption) bool {
	return agentcoder.ServiceTierAllowedForACPModel(serviceTier, modelKey, options)
}

func reasoningEffortAllowedForACPModel(reasoningEffort string, modelKey string, options []api.CoderModelOption) bool {
	return agentcoder.ReasoningEffortAllowedForACPModel(reasoningEffort, modelKey, options)
}

func applyQueryModelOptionsToRawStageSettings(raw map[string]any, modelKey string, reasoningEffort string) map[string]any {
	out := contracts.CloneMap(raw)
	if out == nil {
		out = map[string]any{}
	}
	if modelKey != "" {
		out["modelKey"] = modelKey
	}
	if reasoningEffort == "NONE" {
		out["reasoningEnabled"] = false
		delete(out, "reasoningEffort")
	} else if reasoningEffort != "" {
		out["reasoningEnabled"] = true
		out["reasoningEffort"] = reasoningEffort
	}
	for _, stage := range []string{"plan", "execute", "summary"} {
		nested := contracts.CloneMap(contracts.AnyMapNode(out[stage]))
		if nested == nil {
			nested = map[string]any{}
		}
		if modelKey != "" {
			nested["modelKey"] = modelKey
		}
		if reasoningEffort == "NONE" {
			nested["reasoningEnabled"] = false
			delete(nested, "reasoningEffort")
		} else if reasoningEffort != "" {
			nested["reasoningEnabled"] = true
			nested["reasoningEffort"] = reasoningEffort
		}
		out[stage] = nested
	}
	return out
}

func applyQueryModelOptionsToResolvedStageSettings(settings contracts.PlanExecuteSettings, modelKey string, reasoningEffort string) contracts.PlanExecuteSettings {
	apply := func(stage *contracts.StageSettings) {
		if modelKey != "" {
			stage.ModelKey = modelKey
		}
		if reasoningEffort == "NONE" {
			stage.ReasoningEnabled = false
			stage.ReasoningEffort = ""
		} else if reasoningEffort != "" {
			stage.ReasoningEnabled = true
			stage.ReasoningEffort = reasoningEffort
		}
	}
	apply(&settings.Plan)
	apply(&settings.Execute)
	apply(&settings.Summary)
	return settings
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

func cloneIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func runtimeAgentEnv(value any) map[string]string {
	switch env := value.(type) {
	case map[string]string:
		return contracts.CloneStringMap(env)
	default:
		return nil
	}
}

func resolveSkillRuntimeSettings(agentEnv map[string]string, agentDir string, marketDir string, skillKeys []string) ([]string, map[string]string) {
	runtimeEnv := contracts.CloneStringMap(agentEnv)
	if len(skillKeys) == 0 {
		return nil, runtimeEnv
	}
	seen := map[string]struct{}{}
	var hookDirs []string
	for _, raw := range skillKeys {
		skillKey := strings.ToLower(strings.TrimSpace(raw))
		if skillKey == "" {
			continue
		}
		if _, ok := seen[skillKey]; ok {
			continue
		}
		seen[skillKey] = struct{}{}
		def, ok, err := catalog.ResolveSkillDefinition(agentDir, marketDir, skillKey)
		if err != nil {
			log.Printf("[server][skill-runtime][warn] skill resolution failed key=%s err=%v", skillKey, err)
			continue
		}
		if !ok {
			log.Printf("[server][skill-runtime][warn] skill definition not found key=%s", skillKey)
			continue
		}
		if strings.TrimSpace(def.BashHooksDir) != "" {
			hookDirs = append(hookDirs, def.BashHooksDir)
		}
		for key, value := range def.RuntimeEnv {
			if runtimeEnv == nil {
				runtimeEnv = make(map[string]string, len(agentEnv)+len(def.RuntimeEnv))
			}
			runtimeEnv[key] = value
		}
	}
	return hookDirs, runtimeEnv
}

func sortedStringKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) newAssemblerAndMapper(prepared preparedQuery) (*stream.StreamEventAssembler, contracts.StreamDeltaMapper) {
	role, _ := normalizeQueryRole(prepared.req.Role)
	sceneRef := (*stream.SceneRef)(nil)
	if prepared.req.Scene != nil {
		sceneRef = &stream.SceneRef{
			URL:   prepared.req.Scene.URL,
			Title: prepared.req.Scene.Title,
		}
	}
	assembler := stream.NewAssembler(stream.StreamRequest{
		RequestID:          prepared.req.RequestID,
		RunID:              prepared.req.RunID,
		ChatID:             prepared.req.ChatID,
		ChatName:           prepared.summary.ChatName,
		AgentKey:           prepared.req.AgentKey,
		Message:            prepared.req.Message,
		Role:               role,
		Scene:              sceneRef,
		References:         prepared.req.References,
		Params:             prepared.req.Params,
		Model:              prepared.req.Model,
		PlanningMode:       prepared.session.PlanningMode,
		IncludeUsage:       prepared.req.IncludeUsage,
		IncludeFullText:    prepared.req.IncludeFullText,
		AccessLevel:        prepared.session.AccessLevel,
		Created:            prepared.created,
		ContinueRun:        prepared.continueRun,
		MemoryUsageSummary: memoryUsageEventPayload(prepared.memoryUsageSummary, prepared.req.ChatID, prepared.req.RunID, prepared.req.AgentKey),
	})
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			if cv, ok := toolDef.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(toolDef.Name, toolDef.Key)
			}
		}
	}
	var mapper contracts.StreamDeltaMapper
	if s.deps.DeltaMappers != nil {
		mapper = s.deps.DeltaMappers.NewDeltaMapper(prepared.req.RunID, prepared.req.ChatID, prepared.session.ResolvedBudget, s.toolLookup())
	}
	return assembler, mapper
}
