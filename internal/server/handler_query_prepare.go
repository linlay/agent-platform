package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/memory"
	"agent-platform/internal/stream"
	platformws "agent-platform/internal/ws"
)

type preparedQuery struct {
	req                api.QueryRequest
	summary            chat.Summary
	created            bool
	agentDef           catalog.AgentDefinition
	teamSnapshot       *catalog.TeamSnapshot
	session            contracts.QuerySession
	memoryUsageSummary *api.MemoryUsageSummary
	systemInitLine     *chat.QueryLineSystem
	resourceBaseURL    string
	release            queryReleaseFunc
	continueRun        bool
	initialSeq         int64
	syntheticBootstrap *stream.SyntheticQuery
	execution          *queryExecutionOptions
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
	if prepared.execution == nil {
		return queryExecutionOptions{
			StepLineStore:   s.deps.Chats,
			CompletionStore: s.deps.Chats,
		}
	}
	resolved := *prepared.execution
	if resolved.StepLineStore == nil {
		resolved.StepLineStore = s.deps.Chats
	}
	return resolved
}

type queryAdmission struct {
	req              api.QueryRequest
	existingSummary  *chat.Summary
	agentDef         catalog.AgentDefinition
	teamSnapshot     *catalog.TeamSnapshot
	orchestratedTeam bool
	resourceBaseURL  string
	locale           string
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
	req.ChatSource = chatSourceFromContext(r.Context())
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
		var summaryErr error
		existingSummary, summaryErr = s.deps.Chats.Summary(chatID)
		if summaryErr != nil {
			// A historical chat is an input to a new run as soon as its owner,
			// memory scope, or history is resolved. Do not ignore a malformed
			// timestamp here and accidentally treat the chat as a fresh one.
			return queryAdmission{}, summaryErr
		}
	}
	if gateErr := s.awaitingQueryGateError(chatID, existingSummary); gateErr != nil {
		return queryAdmission{}, gateErr
	}
	teamID, agentKey, teamSnapshot, teamErr := resolveQueryTeam(
		s.deps.Registry,
		req.TeamID,
		req.AgentKey,
		existingSummary,
	)
	if teamErr != nil {
		return queryAdmission{}, teamErr
	}
	orchestratedTeam := teamSnapshot != nil
	if !orchestratedTeam && agentKey == "" && existingSummary != nil {
		agentKey = existingSummary.AgentKey
	}
	if !orchestratedTeam && agentKey == "" {
		agentKey = s.deps.Registry.DefaultAgentKey()
	}
	var agentDef catalog.AgentDefinition
	var found bool
	if orchestratedTeam {
		agentDef = buildTeamCoordinatorDefinition(*teamSnapshot)
		found = true
	} else {
		agentDef, found = s.deps.Registry.AgentDefinition(agentKey)
		if !found {
			return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
		}
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
	if req.PlanningMode != nil && *req.PlanningMode && !agentcoder.IsMode(agentDef.Mode) {
		return queryAdmission{}, &statusError{status: http.StatusBadRequest, message: "planningMode is only supported for CODER agents"}
	}

	req.ChatID = chatID
	req.AgentKey = agentKey
	req.RequestID = requestID
	req.RunID = runID
	req.TeamID = teamID

	return queryAdmission{
		req:              req,
		existingSummary:  existingSummary,
		agentDef:         agentDef,
		teamSnapshot:     teamSnapshot,
		orchestratedTeam: orchestratedTeam,
		resourceBaseURL:  requestBaseURL(r),
		locale:           locale,
	}, nil
}

func (s *Server) completeQueryPreparation(ctx context.Context, admission queryAdmission, release queryReleaseFunc) (preparedQuery, error) {
	req := admission.req
	agentDef := admission.agentDef
	chatID := req.ChatID
	agentKey := req.AgentKey
	chatSource := queryChatSource(ctx, req)
	persistedAgentMode := chatAgentMode(agentDef, admission.orchestratedTeam)
	summary, created, err := s.deps.Chats.EnsureChatWithSourceAndMode(chatID, agentKey, req.TeamID, req.Message, chatSource, persistedAgentMode)
	if err != nil {
		return preparedQuery{}, err
	}
	if !created && strings.TrimSpace(summary.TeamID) != strings.TrimSpace(req.TeamID) {
		return preparedQuery{}, &statusError{
			status:  http.StatusConflict,
			code:    "team_conflict",
			message: "teamId does not match chat",
		}
	}
	if !admission.orchestratedTeam && !created && agentKey != "" {
		if err := s.deps.Chats.UpdateAgentIdentity(chatID, agentKey, persistedAgentMode); err != nil {
			return preparedQuery{}, err
		}
		summary.AgentKey = agentKey
		summary.AgentMode = persistedAgentMode
	}
	if created {
		// automation/system role 只影响 chat 内部 request.query 的展示语义，
		// 不影响会话在列表里的可见性。
		s.broadcast("chat.created", chatCreatedPayload(chatID, summary.ChatName, agentKey, summary.CreatedAt, summary.Source))
	}
	sessionReq := req
	if admission.orchestratedTeam {
		sessionReq.AgentKey = agentDef.Key
	}
	session, err := s.BuildQuerySession(ctx, sessionReq, summary, agentDef, querySessionBuildOptions{
		Created:                created,
		Locale:                 admission.locale,
		IncludeHistory:         !created,
		IncludeMemory:          true,
		AllowInvokeAgents:      resolvedModeCapabilities(agentDef).InvokeChildren,
		TeamCoordinatorHistory: admission.orchestratedTeam,
	})
	if err != nil {
		return preparedQuery{}, err
	}
	if admission.orchestratedTeam && admission.teamSnapshot != nil {
		if s.deps.Tools == nil {
			return preparedQuery{}, fmt.Errorf("Team coordinator tool registry is unavailable")
		}
		baseTool, found := teamDelegateBaseDefinition(s.deps.Tools.Definitions())
		if !found {
			return preparedQuery{}, fmt.Errorf("embedded Team tool %q is unavailable", agentteam.ToolDelegate)
		}
		if err := configureTeamCoordinatorSession(&session, *admission.teamSnapshot, baseTool); err != nil {
			return preparedQuery{}, err
		}
	}
	req.References = session.RuntimeContext.References
	if !isProxyAgentMode(agentDef.Mode) {
		applyQueryModelOptionsToSession(req.Model, &session)
	}
	sessionReq.References = req.References
	session.CurrentMessages = s.buildCurrentMessages(sessionReq, session)
	if !created {
		s.maybeAutoCompact(ctx, req, agentDef, &session)
	}
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	systemInitLine, err := s.prepareSystemInitCache(sessionReq, &session, created)
	if err != nil {
		return preparedQuery{}, err
	}

	return preparedQuery{
		req:                req,
		summary:            summary,
		created:            created,
		agentDef:           agentDef,
		teamSnapshot:       admission.teamSnapshot,
		session:            session,
		memoryUsageSummary: session.MemoryUsageSummary,
		systemInitLine:     systemInitLine,
		resourceBaseURL:    admission.resourceBaseURL,
		release:            release,
	}, nil
}

func chatAgentMode(agentDef catalog.AgentDefinition, orchestratedTeam bool) string {
	if orchestratedTeam {
		return "TEAM"
	}
	return catalog.AgentModeForAPI(agentDef.Mode)
}

func queryChatSource(ctx context.Context, req api.QueryRequest) string {
	source := strings.TrimSpace(req.ChatSource)
	if source != "" {
		return source
	}
	return queryChatSourceForUser(querySourceUser(ctx, req))
}

func queryChatSourceForUser(user string) string {
	user = normalizeChatSourcePart(user)
	if user == "" {
		return api.ChatSourceQuery
	}
	return api.ChatSourceQueryPrefix + user
}

func querySourceUser(ctx context.Context, req api.QueryRequest) string {
	if _, ok := platformws.GatewayFromContext(ctx); ok {
		if user := strings.TrimSpace(req.SourceUser); user != "" {
			return user
		}
		if user := channelUserFromChatID(req.ChatID); user != "" {
			return user
		}
	}
	if principal := PrincipalFromContext(ctx); principal != nil && strings.TrimSpace(principal.Subject) != "" {
		return principal.Subject
	}
	if user := channelUserFromChatID(req.ChatID); user != "" {
		return user
	}
	return ""
}

func channelUserFromChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || channel.ChannelForChatID(chatID) == "" {
		return ""
	}
	parts := strings.Split(chatID, "#")
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

func normalizeChatSourcePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 160 {
		value = string(runes[:160])
	}
	return strings.TrimSpace(value)
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
	if isProxyAgentMode(agentDef.Mode) || catalog.AgentIsChannelMode(agentDef.Mode) {
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
	session.StageSettings = applyQueryModelOptionsToRawStageSettings(session.Mode, session.StageSettings, modelKey, reasoningEffort)
	session.ResolvedPlanExecuteSettings = applyQueryModelOptionsToResolvedPlanExecuteSettings(session.ResolvedPlanExecuteSettings, modelKey, reasoningEffort)
	session.ResolvedCoderPlanningSettings = applyQueryModelOptionsToResolvedCoderPlanningSettings(session.ResolvedCoderPlanningSettings, modelKey, reasoningEffort)
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

func applyQueryModelOptionsToRawStageSettings(mode string, raw map[string]any, modelKey string, reasoningEffort string) map[string]any {
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
	stages := []string{"plan", "execute", "summary"}
	if agentcoder.IsMode(mode) {
		stages = []string{"planning", "execute"}
	}
	for _, stage := range stages {
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

func applyQueryModelOptionsToResolvedPlanExecuteSettings(settings contracts.PlanExecuteSettings, modelKey string, reasoningEffort string) contracts.PlanExecuteSettings {
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

func applyQueryModelOptionsToResolvedCoderPlanningSettings(settings contracts.CoderPlanningSettings, modelKey string, reasoningEffort string) contracts.CoderPlanningSettings {
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
	apply(&settings.Planning)
	apply(&settings.Execute)
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
	execution := s.resolvedQueryExecution(prepared)
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
		TeamID:             prepared.req.TeamID,
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
		InitialSeq:         prepared.initialSeq,
		BootstrapSynthetic: prepared.syntheticBootstrap,
		MemoryUsageSummary: memoryUsageEventPayload(prepared.memoryUsageSummary, prepared.req.ChatID, prepared.req.RunID, prepared.req.AgentKey),
		QueryMetadata:      contracts.CloneMap(execution.QueryMetadata),
	})
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			if cv, ok := toolDef.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(toolDef.Name, toolDef.Key)
			}
		}
	}
	for _, toolDef := range prepared.session.ModeToolDefinitions {
		if cv, ok := toolDef.Meta["clientVisible"].(bool); ok && !cv {
			assembler.RegisterHiddenTools(toolDef.Name, toolDef.Key)
		}
	}
	var mapper contracts.StreamDeltaMapper
	if s.deps.DeltaMappers != nil {
		mapper = s.deps.DeltaMappers.NewDeltaMapper(prepared.req.RunID, prepared.req.ChatID, prepared.session.ResolvedBudget, s.toolLookup())
	}
	return assembler, mapper
}
