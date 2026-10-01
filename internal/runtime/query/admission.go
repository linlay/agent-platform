package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/proxy"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func reasoningEffortAllowedForACPModel(reasoningEffort string, modelKey string, options []queryinput.CoderModelOption) bool {
	return agentbuiltin.CoderReasoningEffortAllowedForACPModel(reasoningEffort, modelKey, options)
}

func (s *Service) resolvedQueryExecution(prepared preparedQuery) queryExecutionOptions {
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

func ApplyQueryModelOptionsToSession(options *queryinput.QueryModelOptions, session *contracts.QuerySession) {
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

type queryAdmission struct {
	Req              runtimetypes.QueryCommand
	ExistingSummary  *chat.Summary
	AgentDef         catalog.AgentDefinition
	TeamSnapshot     *catalog.TeamSnapshot
	OrchestratedTeam bool
	ResourceBaseURL  string
	Locale           string
	StrictOwner      bool
	Release          queryReleaseFunc
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

func serviceTierAllowedForACPModel(serviceTier string, modelKey string, options []queryinput.CoderModelOption) bool {
	return agentbuiltin.CoderServiceTierAllowedForACPModel(serviceTier, modelKey, options)
}

func (s *Service) newAssemblerAndMapper(prepared preparedQuery) (*stream.StreamEventAssembler, contracts.StreamDeltaMapper) {
	execution := s.resolvedQueryExecution(prepared)
	role, _ := normalizeQueryRole(prepared.Req.Role)
	sceneRef := (*stream.SceneRef)(nil)
	if prepared.Req.Scene != nil {
		sceneRef = &stream.SceneRef{
			URL:   prepared.Req.Scene.URL,
			Title: prepared.Req.Scene.Title,
		}
	}
	assembler := stream.NewAssembler(stream.StreamRequest{
		RequestID:          prepared.Req.RequestID,
		RunID:              prepared.Req.RunID,
		ChatID:             prepared.Req.ChatID,
		ChatName:           prepared.Summary.ChatName,
		AgentKey:           prepared.Req.AgentKey,
		TeamID:             prepared.Req.TeamID,
		Message:            prepared.Req.Message,
		Role:               role,
		Hidden:             prepared.Req.Hidden,
		Scene:              sceneRef,
		References:         prepared.Req.References,
		Params:             prepared.Req.Params,
		Model:              prepared.Req.Model,
		PlanningMode:       prepared.Session.PlanningMode,
		EditingMode:        prepared.Session.EditingMode,
		MustUseSkills:      prepared.Session.MustUseSkills,
		IncludeUsage:       prepared.Req.IncludeUsage,
		IncludeFullText:    prepared.Req.IncludeFullText,
		AccessLevel:        prepared.Session.AccessLevel,
		Created:            prepared.Created,
		ContinueRun:        prepared.ContinueRun,
		InitialSeq:         prepared.InitialSeq,
		BootstrapSynthetic: prepared.SyntheticBootstrap,
		MemoryUsageSummary: memoryUsageEventPayload(prepared.MemoryUsageSummary, prepared.Req.ChatID, prepared.Req.RunID, prepared.Req.AgentKey),
		QueryMetadata:      contracts.CloneMap(execution.QueryMetadata),
	})
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			if cv, ok := toolDef.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(toolDef.Name, toolDef.Key)
			}
		}
	}
	for _, toolDef := range prepared.Session.ModeToolDefinitions {
		if cv, ok := toolDef.Meta["clientVisible"].(bool); ok && !cv {
			assembler.RegisterHiddenTools(toolDef.Name, toolDef.Key)
		}
	}
	var mapper contracts.StreamDeltaMapper
	if s.deps.DeltaMappers != nil {
		mapper = s.deps.DeltaMappers.NewDeltaMapper(prepared.Req.RunID, prepared.Req.ChatID, prepared.Session.ResolvedBudget, s.toolLookup())
	}
	return assembler, mapper
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
	if agentbuiltin.IsCoderMode(mode) {
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

func normalizeQueryModelReasoningEffort(value string) (string, bool) {
	return agentbuiltin.CoderNormalizeReasoningEffort(value)
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

func (s *Service) ValidateQueryModelOptions(options *queryinput.QueryModelOptions, agentDef catalog.AgentDefinition) error {
	if options == nil {
		return nil
	}
	if sessionbuild.IsProxyAgentMode(agentDef.Mode) || catalog.AgentIsChannelMode(agentDef.Mode) {
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
			return &statusError{Status: 503, Message: "model registry is not configured"}
		}
		if catalog.AgentUsesACPCoderBackend(agentDef) {
			options, err, ok := s.deps.Proxy.Models(agentDef.Key)
			if ok {
				if err != nil {
					return &statusError{Status: 502, Message: "failed to fetch ACP CODER models: " + err.Error()}
				}
				if !agentbuiltin.CoderModelKeyInOptions(modelKey, options) {
					return &statusError{Status: 400, Message: "model " + modelKey + " is not available for ACP CODER"}
				}
			} else if err := s.validateLocalChatModelKey(modelKey, false); err != nil {
				return &statusError{Status: 400, Message: err.Error()}
			}
		} else {
			if err := s.validateLocalChatModelKey(modelKey, true); err != nil {
				return &statusError{Status: 400, Message: err.Error()}
			}
		}
	}
	reasoningEffort, ok := normalizeQueryModelReasoningEffort(reasoningEffort)
	if !ok {
		return &statusError{Status: 400, Message: "model.reasoningEffort must be NONE, LOW, MEDIUM, HIGH, XHIGH, or MAX"}
	}
	options.ReasoningEffort = reasoningEffort
	if reasoningEffort != "" && reasoningEffort != "NONE" && catalog.AgentUsesACPCoderBackend(agentDef) {
		acpOptions, err, listed := s.deps.Proxy.Models(agentDef.Key)
		if listed {
			if err != nil {
				return &statusError{Status: 502, Message: "failed to fetch ACP CODER models: " + err.Error()}
			}
			if !reasoningEffortAllowedForACPModel(reasoningEffort, modelKey, acpOptions) {
				return &statusError{Status: 400, Message: "model.reasoningEffort " + reasoningEffort + " is not available for ACP CODER"}
			}
		}
	}
	serviceTier, ok = normalizeQueryModelServiceTier(serviceTier)
	if !ok {
		return &statusError{Status: 400, Message: "model.serviceTier must be a non-empty string"}
	}
	if serviceTier != "" {
		if !catalog.AgentUsesACPCoderBackend(agentDef) {
			return &statusError{Status: 400, Message: "model.serviceTier is only supported for ACP CODER"}
		}
		acpOptions, err, listed := s.deps.Proxy.Models(agentDef.Key)
		if listed {
			if err != nil {
				return &statusError{Status: 502, Message: "failed to fetch ACP CODER models: " + err.Error()}
			}
			if !serviceTierAllowedForACPModel(serviceTier, modelKey, acpOptions) {
				return &statusError{Status: 400, Message: "model.serviceTier " + serviceTier + " is not available for ACP CODER"}
			}
		}
	}
	return nil
}

type preparedQuery = runtimetypes.PreparedQuery

func combineQueryReleases(releases ...queryReleaseFunc) queryReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, release := range releases {
				if release != nil {
					release()
				}
			}
		})
	}
}

func querySourceUser(ctx context.Context, req runtimetypes.QueryCommand) string {
	if req.TrustedGateway {
		if user := strings.TrimSpace(req.SourceUser); user != "" {
			return user
		}
		if user := channelUserFromChatID(req.ChatID); user != "" {
			return user
		}
	}
	if principal := runtimetypes.IdentityFromContext(ctx); principal != nil && strings.TrimSpace(principal.Subject) != "" {
		return principal.Subject
	}
	if user := channelUserFromChatID(req.ChatID); user != "" {
		return user
	}
	return ""
}

func applyDesktopImageStudioRunLimits(req runtimetypes.QueryCommand, session *contracts.QuerySession) {
	if session == nil || strings.TrimSpace(req.AgentKey) != "zenmi" {
		return
	}
	desktop, ok := req.Params["desktop"].(map[string]any)
	if !ok || strings.TrimSpace(fmt.Sprint(desktop["source"])) != "copilot" ||
		strings.TrimSpace(fmt.Sprint(desktop["action"])) != "image_studio" {
		return
	}
	session.RunLimits.MaxToolCalls = 1
	session.RunLimits.MaxToolRounds = 1
	session.RunLimits.FinalAnswerPrompt = "The Image Studio task has reached its only permitted tool round. Do not call any tool again; report the existing tool result accurately."
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

func (s *Service) PrepareQueryAdmissionRequest(
	ctx context.Context,
	req runtimetypes.QueryCommand,
	requireMessage bool,
	locale string,
	resourceBaseURL string,
) (result queryAdmission, resultErr error) {
	if requireMessage && strings.TrimSpace(req.Message) == "" && len(req.References) > 0 && !hasQueryReferenceContent(req.References) {
		return queryAdmission{}, &statusError{Status: 400, Message: "message is required"}
	}
	if role, ok := normalizeQueryRole(req.Role); ok {
		req.Role = role
	} else {
		return queryAdmission{}, &statusError{Status: 400, Message: queryinput.QueryRoleValidationMessage}
	}
	req.ChatSource = runtimetypes.ChatSourceFromContext(ctx)
	accessLevel, ok := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !ok {
		return queryAdmission{}, &statusError{Status: 400, Message: "accessLevel must be default, auto_approve, or full_access"}
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
	var admissionRelease queryReleaseFunc
	defer func() {
		if resultErr != nil || result.Release == nil {
			releaseQuery(admissionRelease)
		}
	}()
	if reservations, ok := s.deps.Runs.(contracts.ChatQueryAdmissionService); ok {
		release, reserveErr := reservations.ReserveChatQuery(chatID, requestID)
		if reserveErr != nil {
			var maintenanceErr *contracts.ChatMaintenanceConflictError
			if errors.As(reserveErr, &maintenanceErr) {
				return queryAdmission{}, &statusError{
					Status:  409,
					Code:    "compact_in_progress",
					Message: "context compaction is in progress",
					Data: map[string]any{"error": map[string]any{
						"code": "compact_in_progress", "message": "context compaction is in progress", "retryable": true,
					}},
				}
			}
			return queryAdmission{}, reserveErr
		}
		admissionRelease = release
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
	if requireMessage && strings.TrimSpace(req.Message) == "" {
		hasHistory := existingSummary != nil && strings.TrimSpace(existingSummary.LastRunID) != ""
		if !hasHistory && existingSummary != nil {
			// An accepted query can exist before its first run reaches a terminal state.
			detail, err := s.deps.Chats.LoadChat(chatID)
			if err != nil {
				return queryAdmission{}, err
			}
			for _, event := range detail.Events {
				if event.Type == "request.query" && event.Value("hidden") != true &&
					(event.String("lane") == "" || event.String("lane") == "main") {
					hasHistory = true
					break
				}
			}
		}
		if !hasHistory {
			return queryAdmission{}, &statusError{Status: 400, Message: "message is required for the first query"}
		}
	}
	if gateErr := s.AwaitingQueryGateError(chatID, existingSummary); gateErr != nil {
		return queryAdmission{}, gateErr
	}
	if requireMessage && strings.TrimSpace(req.Message) == "" && len(req.References) == 0 &&
		(existingSummary == nil || !existingSummary.CanContinue) {
		return queryAdmission{}, &statusError{Status: 400, Code: "empty_query_not_allowed", Message: "empty query requires the last run to have failed or been canceled"}
	}

	teamID, agentKey, teamSnapshot, teamErr := ResolveQueryTeam(
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
		leasedTeam, release, ok := catalogview.AcquireTeam(s.deps.Registry, teamID)
		if !ok {
			return queryAdmission{}, fmt.Errorf("team runtime is unavailable")
		}
		admissionRelease = combineQueryReleases(admissionRelease, release)
		teamSnapshot = &leasedTeam
		agentDef = buildTeamCoordinatorDefinition(*teamSnapshot)
		found = true
	} else {
		var release func()
		agentDef, release, found = catalogview.AcquireAgent(s.deps.Registry, agentKey)
		admissionRelease = combineQueryReleases(admissionRelease, release)
		if !found {
			if registry, ok := s.deps.Registry.(interface {
				AdminAgent(string) (catalog.AdminAgent, bool)
			}); ok {
				if agent, exists := registry.AdminAgent(agentKey); exists && agent.Status == catalog.AdminAgentStatusInvalid {
					return queryAdmission{}, apperrors.New(apperrors.CodeAgentConfigurationInvalid, "The agent configuration is invalid. Fix it in agent management before sending again.")
				}
			}
			return queryAdmission{}, apperrors.New(apperrors.CodeAgentNotFound, "The agent is unavailable. Select an available agent.")
		}
	}
	if sessionbuild.IsProxyRoutedAgent(agentDef) && proxy.RequestHasReservedCWD(req.Params) {
		return queryAdmission{}, &statusError{
			Status:  400,
			Message: "params.cwd is reserved for proxy-routed agents; configure runtimeConfig.workspaceRoot in agent.yml",
		}
	}
	if statusErr := s.deps.Proxy.Configure(&agentDef); statusErr != nil {
		return queryAdmission{}, statusErr
	}
	if !orchestratedTeam && !sessionbuild.IsProxyAgentMode(agentDef.Mode) && !catalog.AgentIsChannelMode(agentDef.Mode) {
		if err := ValidateInteractionInput(agentDef.Interaction(), req); err != nil {
			return queryAdmission{}, err
		}
	}
	if err := s.ValidateQueryModelOptions(req.Model, agentDef); err != nil {
		return queryAdmission{}, err
	}
	if req.PlanningMode != nil && *req.PlanningMode && !agentbuiltin.IsCoderMode(agentDef.Mode) {
		return queryAdmission{}, &statusError{Status: 400, Message: "planningMode is only supported for CODER agents"}
	}
	if req.EditingMode != nil && *req.EditingMode && !agentbuiltin.IsKBaseMode(agentDef.Mode) {
		const code = "editing_mode_unsupported"
		const message = "editingMode is only supported for dedicated KBASE agents"
		return queryAdmission{}, &statusError{
			Status:  400,
			Code:    code,
			Message: message,
			Data: map[string]any{
				"error": map[string]any{"code": code, "message": message},
			},
		}
	}
	req.MustUseSkills = sessionbuild.NormalizeMustUseSkills(req.MustUseSkills)
	if orchestratedTeam && len(req.MustUseSkills) > 0 {
		const code = "must_use_skills_unsupported"
		const message = "mustUseSkills is not supported for Team runs"
		return queryAdmission{}, &statusError{
			Status:  400,
			Code:    code,
			Message: message,
			Data: map[string]any{
				"error": map[string]any{"code": code, "message": message},
			},
		}
	}
	mustUseSkills, err := s.deps.Sessions.ResolveSkills(agentDef, req.MustUseSkills)
	if err != nil {
		return queryAdmission{}, sessionbuild.MustUseSkillUnavailableStatus(err)
	}
	req.MustUseSkills = mustUseSkills.IDs
	preparedReferences, err := s.deps.References.Prepare(ctx, chatID, req.References)
	if err != nil {
		return queryAdmission{}, err
	}
	req.References = preparedReferences

	req.ChatID = chatID
	req.AgentKey = agentKey
	req.RequestID = requestID
	req.RunID = runID
	req.TeamID = teamID

	return queryAdmission{
		Req:              req,
		ExistingSummary:  existingSummary,
		AgentDef:         agentDef,
		TeamSnapshot:     teamSnapshot,
		OrchestratedTeam: orchestratedTeam,
		ResourceBaseURL:  resourceBaseURL,
		Locale:           locale,
		Release:          admissionRelease,
	}, nil
}

func chatAgentMode(agentDef catalog.AgentDefinition, orchestratedTeam bool) string {
	if orchestratedTeam {
		return "TEAM"
	}
	return catalog.AgentModeForAPI(agentDef.Mode)
}

func hasQueryReferenceContent(references []runtimetypes.Reference) bool {
	for _, reference := range references {
		switch strings.ToLower(strings.TrimSpace(reference.Type)) {
		case "selection":
			if _, err := queryinput.NormalizeSelectionReference(reference); err == nil {
				return true
			}
		case "", "file":
			if strings.TrimSpace(reference.URL) != "" {
				return true
			}
		}
	}
	return false
}

type queryExecutionOptions = runtimetypes.QueryExecutionOptions

func normalizeQueryModelServiceTier(value string) (string, bool) {
	return agentbuiltin.CoderNormalizeServiceTier(value)
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

func queryChatSourceForUser(user string) string {
	user = normalizeChatSourcePart(user)
	if user == "" {
		return queryinput.ChatSourceQuery
	}
	return queryinput.ChatSourceQueryPrefix + user
}

type queryReleaseFunc = func()

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}

func (s *Service) CompleteQueryPreparation(ctx context.Context, admission queryAdmission, release queryReleaseFunc) (preparedQuery, error) {
	combinedRelease := combineQueryReleases(admission.Release, release)
	succeeded := false
	defer func() {
		if !succeeded {
			releaseQuery(combinedRelease)
		}
	}()
	req := admission.Req
	agentDef := admission.AgentDef
	chatID := req.ChatID
	agentKey := req.AgentKey
	chatSource := queryChatSource(ctx, req)
	persistedAgentMode := chatAgentMode(agentDef, admission.OrchestratedTeam)
	summary, created, err := s.deps.Chats.EnsureChatWithSourceAndMode(chatID, agentKey, req.TeamID, req.Message, chatSource, persistedAgentMode)
	if err != nil {
		return preparedQuery{}, err
	}
	if admission.StrictOwner && !created && !runOwnerMatchesChat(&summary, agentKey, req.TeamID) {
		return preparedQuery{}, &statusError{
			Status:  409,
			Code:    "target_owner_mismatch",
			Message: "target identity does not match chat owner",
		}
	}
	if !created && strings.TrimSpace(summary.TeamID) != strings.TrimSpace(req.TeamID) {
		return preparedQuery{}, &statusError{
			Status:  409,
			Code:    "team_conflict",
			Message: "teamId does not match chat",
		}
	}
	if !admission.OrchestratedTeam && !created && agentKey != "" {
		if err := s.deps.Chats.UpdateAgentIdentity(chatID, agentKey, persistedAgentMode); err != nil {
			return preparedQuery{}, err
		}
		summary.AgentKey = agentKey
		summary.AgentMode = persistedAgentMode
	}
	chatNamePromoted := false
	if !created {
		summary, chatNamePromoted, err = s.deps.Chats.PromotePendingChatName(chatID, req.Message)
		if err != nil {
			return preparedQuery{}, err
		}
	}
	if created {
		// automation/system role 只影响 chat 内部 request.query 的展示语义，
		// 不影响会话在列表里的可见性。
		s.broadcast("chat.created", chatCreatedPayload(chatID, summary.ChatName, agentKey, summary.CreatedAt, summary.Source))
	} else if chatNamePromoted {
		s.broadcast("chat.renamed", map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName, "agentKey": summary.AgentKey})
	}
	sessionReq := req
	if admission.OrchestratedTeam {
		sessionReq.AgentKey = agentDef.Key
	}
	session, err := s.deps.Sessions.BuildQuerySession(ctx, sessionReq, summary, agentDef, sessionbuild.Options{
		Created:                created,
		Locale:                 admission.Locale,
		IncludeHistory:         !created,
		IncludeMemory:          true,
		AllowInvokeAgents:      sessionbuild.ResolvedModeCapabilities(agentDef).InvokeChildren,
		TeamCoordinatorHistory: admission.OrchestratedTeam,
	})
	if err != nil {
		if errors.Is(err, chat.ErrChatHistoryIncomplete) {
			payload := apperrors.Payload(
				apperrors.CodeChatHistoryIncomplete,
				err.Error(),
				apperrors.WithDiagnostic("chatId", chatID),
			)
			return preparedQuery{}, &statusError{
				Status:  409,
				Code:    string(apperrors.CodeChatHistoryIncomplete),
				Message: err.Error(),
				Data:    map[string]any{"error": payload},
			}
		}
		return preparedQuery{}, err
	}
	applyDesktopImageStudioRunLimits(req, &session)
	if admission.OrchestratedTeam && admission.TeamSnapshot != nil {
		if s.deps.Tools == nil {
			return preparedQuery{}, fmt.Errorf("Team coordinator tool registry is unavailable")
		}
		baseTool, found := TeamDelegateBaseDefinition(s.deps.Tools.Definitions())
		if !found {
			return preparedQuery{}, fmt.Errorf("embedded Team tool %q is unavailable", agentbuiltin.TeamToolDelegate)
		}
		if err := ConfigureTeamCoordinatorSession(&session, *admission.TeamSnapshot, baseTool); err != nil {
			return preparedQuery{}, err
		}
	}
	req.References = session.RuntimeContext.References
	if !sessionbuild.IsProxyAgentMode(agentDef.Mode) {
		ApplyQueryModelOptionsToSession(req.Model, &session)
	}
	sessionReq.References = req.References
	session.CurrentMessages = s.deps.Sessions.BuildCurrentMessages(sessionReq, session)
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	systemInitLine, err := s.deps.Sessions.PrepareSystemInitCache(sessionReq, &session, created)
	if err != nil {
		return preparedQuery{}, err
	}

	prepared := preparedQuery{
		Req:                req,
		Summary:            summary,
		Created:            created,
		AgentDef:           agentDef,
		TeamSnapshot:       admission.TeamSnapshot,
		Session:            session,
		MemoryUsageSummary: session.MemoryUsageSummary,
		SystemInitLine:     systemInitLine,
		ResourceBaseURL:    admission.ResourceBaseURL,
		Release:            combinedRelease,
	}
	succeeded = true
	return prepared, nil
}

func memoryUsageEventPayload(summary *queryinput.MemoryUsageSummary, chatID string, runID string, agentKey string) map[string]any {
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

func queryChatSource(ctx context.Context, req runtimetypes.QueryCommand) string {
	source := strings.TrimSpace(req.ChatSource)
	if source != "" {
		return source
	}
	return queryChatSourceForUser(querySourceUser(ctx, req))
}

// Admission is a staged, leased query input. CompleteQueryPreparation consumes it.
type Admission = queryAdmission
