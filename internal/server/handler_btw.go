package server

import (
	"errors"
	"net/http"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
)

const btwReadOnlyUserInstruction = `[BTW read-only mode]
This is a hidden side conversation. Do not modify files, memory, plans, browser or desktop state, external systems, or the parent chat. Write and mutation tools are disabled. Use only read-only tools. If the request requires a mutation, explain that it cannot be performed in BTW mode.`

func (s *Server) handleBTW(w http.ResponseWriter, r *http.Request) {
	prepared, statusErr := s.prepareBTWQuery(r)
	if statusErr != nil {
		writeStatusError(w, statusErr)
		return
	}
	w.Header().Set("X-Btw-Id", prepared.execution.BTWID)
	w.Header().Set("X-Run-Id", prepared.req.RunID)
	s.handlePreparedLocalQuery(w, r, prepared)
}

func (s *Server) prepareBTWQuery(r *http.Request) (preparedQuery, *statusError) {
	var input api.BTWRequest
	if err := decodeJSON(r, &input); err != nil {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "invalid_btw_request", "invalid request body")
	}
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.BTWID = strings.TrimSpace(input.BTWID)
	input.Message = strings.TrimSpace(input.Message)
	if input.ChatID == "" || !chat.ValidChatID(input.ChatID) {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "invalid_chat_id", "valid chatId is required")
	}
	if input.Message == "" {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "message_required", "message is required")
	}
	if input.BTWID != "" && !chat.ValidBTWID(input.BTWID) {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "invalid_btw_id", "invalid btwId")
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(input.AccessLevel)
	if !ok {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "invalid_access_level", "accessLevel must be default, auto_approve, or full_access")
	}

	summary, err := s.deps.Chats.Summary(input.ChatID)
	if err != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_prepare_failed", err.Error())
	}
	if summary == nil {
		return preparedQuery{}, btwStatusError(http.StatusNotFound, "chat_not_found", "parent chat not found")
	}
	teamID, agentKey, teamSnapshot, teamErr := resolveQueryTeam(
		s.deps.Registry,
		strings.TrimSpace(summary.TeamID),
		"",
		summary,
	)
	if teamErr != nil {
		return preparedQuery{}, teamErr
	}
	var agentDef catalog.AgentDefinition
	if teamSnapshot != nil {
		agentDef, ok = teamSnapshot.AgentDefinition(agentKey)
	} else {
		if agentKey == "" {
			agentKey = s.deps.Registry.DefaultAgentKey()
		}
		agentDef, ok = s.deps.Registry.AgentDefinition(agentKey)
	}
	if !ok {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "agent_not_found", "parent chat agent not found")
	}
	if isProxyRoutedAgent(agentDef) {
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "btw_backend_unsupported", "BTW read-only mode is not supported by this agent backend")
	}
	if err := s.validateQueryModelOptions(input.Model, agentDef); err != nil {
		if typed, ok := err.(*statusError); ok {
			return preparedQuery{}, typed
		}
		return preparedQuery{}, btwStatusError(http.StatusBadRequest, "invalid_model", err.Error())
	}

	repository, ok := s.deps.Chats.(chat.BTWRepository)
	if !ok {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_store_unavailable", "BTW store is not configured")
	}
	btwID := input.BTWID
	created := btwID == ""
	if created {
		btwID = "btw_" + newChatID()
	}
	var branch *chat.BTWBranchStore
	if created {
		branch, err = repository.CreateBTWBranch(input.ChatID, btwID)
	} else {
		branch, err = repository.OpenBTWBranch(input.ChatID, btwID)
	}
	if errors.Is(err, chat.ErrBTWNotFound) {
		return preparedQuery{}, btwStatusError(http.StatusNotFound, "btw_not_found", "BTW branch not found")
	}
	if err != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_store_failed", err.Error())
	}
	keepBranch := false
	if created {
		defer func() {
			if !keepBranch {
				_ = repository.DeleteBTWBranch(input.ChatID, btwID)
			}
		}()
	}

	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = newRunID()
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = runID
	}
	req := api.QueryRequest{
		RequestID:       requestID,
		RunID:           runID,
		ChatID:          input.ChatID,
		AgentKey:        agentKey,
		TeamID:          teamID,
		Role:            api.QueryRoleUser,
		Message:         input.Message,
		References:      input.References,
		Params:          contracts.CloneMap(input.Params),
		Scene:           input.Scene,
		Stream:          input.Stream,
		IncludeUsage:    input.IncludeUsage,
		IncludeFullText: input.IncludeFullText,
		AccessLevel:     accessLevel,
		Model:           input.Model,
	}
	delete(req.Params, agentcoder.PlanApproveContinuationParam)
	session, buildErr := s.BuildQuerySession(r.Context(), req, *summary, agentDef, querySessionBuildOptions{
		Created:           false,
		Locale:            requestLocale(r, i18n.DefaultLocale),
		IncludeHistory:    false,
		IncludeMemory:     true,
		AllowInvokeAgents: resolvedModeCapabilities(agentDef).InvokeChildren,
	})
	if buildErr != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_prepare_failed", buildErr.Error())
	}
	req.References = session.RuntimeContext.References
	history, loadErr := branch.LoadRawMessages(chat.DefaultHistoryRunWindow)
	if loadErr != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_history_failed", loadErr.Error())
	}
	session.HistoryMessages = history
	applyQueryModelOptionsToSession(req.Model, &session)
	session.PlanningMode = false
	session.RunScopeID = "btw:" + input.ChatID + ":" + btwID
	session.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly
	modelReq := req
	modelReq.Message = btwReadOnlyUserInstruction + "\n\n" + input.Message
	session.CurrentMessages = s.buildCurrentMessages(modelReq, session)

	systemInits, loadErr := branch.LoadAllSystemInits()
	if loadErr != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_system_cache_failed", loadErr.Error())
	}
	pendingSystem, cacheErr := s.prepareSystemInitCacheFrom(req, &session, systemInits)
	if cacheErr != nil {
		return preparedQuery{}, btwStatusError(http.StatusInternalServerError, "btw_system_cache_failed", cacheErr.Error())
	}
	summaryCopy := *summary
	summaryCopy.Usage = nil
	summaryCopy.PendingAwaiting = nil
	keepBranch = true
	return preparedQuery{
		req:                req,
		summary:            summaryCopy,
		created:            false,
		agentDef:           agentDef,
		teamSnapshot:       teamSnapshot,
		session:            session,
		memoryUsageSummary: session.MemoryUsageSummary,
		systemInitLine:     pendingSystem,
		resourceBaseURL:    requestBaseURL(r),
		execution: &queryExecutionOptions{
			StepLineStore:   branch,
			CompletionStore: nil,
			HiddenRun:       true,
			BTWID:           btwID,
			ParentChatID:    input.ChatID,
			QueryMetadata: map[string]any{
				"kind":         "btw",
				"btwId":        btwID,
				"parentChatId": input.ChatID,
				"hidden":       true,
			},
		},
	}, nil
}

func btwStatusError(status int, code string, message string) *statusError {
	return &statusError{
		status:  status,
		code:    code,
		message: message,
		data: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
}
