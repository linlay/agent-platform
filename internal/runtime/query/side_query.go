package query

import (
	"context"
	"errors"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/catalogview"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Service) prepareSideQuery(ctx context.Context, input runtimetypes.QueryCommand) (preparedQuery, *statusError) {
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.SideQueryID = strings.TrimSpace(input.SideQueryID)
	input.Message = strings.TrimSpace(input.Message)
	if input.ChatID == "" || !chat.ValidChatID(input.ChatID) {
		return preparedQuery{}, btwStatusError(400, "invalid_chat_id", "valid chatId is required")
	}
	if input.Message == "" {
		return preparedQuery{}, btwStatusError(400, "message_required", "message is required")
	}
	if input.SideQueryID != "" && !chat.ValidBTWID(input.SideQueryID) {
		return preparedQuery{}, btwStatusError(400, "invalid_btw_id", "invalid btwId")
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(input.AccessLevel)
	if !ok {
		return preparedQuery{}, btwStatusError(400, "invalid_access_level", "accessLevel must be default, auto_approve, or full_access")
	}

	summary, err := s.deps.Chats.Summary(input.ChatID)
	if err != nil {
		if statusErr := timeContractStatusError(err); statusErr != nil {
			return preparedQuery{}, statusErr
		}
		return preparedQuery{}, btwStatusError(500, "btw_prepare_failed", err.Error())
	}
	if summary == nil {
		return preparedQuery{}, btwStatusError(404, "chat_not_found", "parent chat not found")
	}
	teamID, agentKey, teamSnapshot, teamErr := ResolveQueryTeam(
		s.deps.Registry,
		strings.TrimSpace(summary.TeamID),
		"",
		summary,
	)
	if teamErr != nil {
		return preparedQuery{}, teamErr
	}
	var agentDef catalog.AgentDefinition
	var releaseRuntime func()
	keepRuntime := false
	defer func() {
		if !keepRuntime {
			releaseQuery(releaseRuntime)
		}
	}()
	if teamSnapshot != nil {
		leasedTeam, release, found := catalogview.AcquireTeam(s.deps.Registry, teamID)
		releaseRuntime, ok = release, found
		if found {
			teamSnapshot = &leasedTeam
			agentDef, ok = leasedTeam.AgentDefinition(agentKey)
		}
	} else {
		if agentKey == "" {
			agentKey = s.deps.Registry.DefaultAgentKey()
		}
		agentDef, releaseRuntime, ok = catalogview.AcquireAgent(s.deps.Registry, agentKey)
	}
	if !ok {
		return preparedQuery{}, btwStatusError(400, "agent_not_found", "parent chat agent not found")
	}
	if sessionbuild.IsProxyRoutedAgent(agentDef) {
		return preparedQuery{}, btwStatusError(400, "btw_backend_unsupported", "BTW read-only mode is not supported by this agent backend")
	}
	if err := s.ValidateQueryModelOptions(input.Model, agentDef); err != nil {
		if typed, ok := err.(*statusError); ok {
			return preparedQuery{}, typed
		}
		return preparedQuery{}, btwStatusError(400, "invalid_model", err.Error())
	}

	repository, ok := s.deps.Chats.(chat.BTWRepository)
	if !ok {
		return preparedQuery{}, btwStatusError(500, "btw_store_unavailable", "BTW store is not configured")
	}
	btwID := input.SideQueryID
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
		return preparedQuery{}, btwStatusError(404, "btw_not_found", "BTW branch not found")
	}
	if err != nil {
		if statusErr := timeContractStatusError(err); statusErr != nil {
			return preparedQuery{}, statusErr
		}
		return preparedQuery{}, btwStatusError(500, "btw_store_failed", err.Error())
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
	req := runtimetypes.QueryCommand{
		RequestID:       requestID,
		RunID:           runID,
		ChatID:          input.ChatID,
		AgentKey:        agentKey,
		TeamID:          teamID,
		Role:            queryinput.QueryRoleUser,
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
	delete(req.Params, agentbuiltin.CoderPlanningApproveContinuationParam)
	session, buildErr := s.deps.Sessions.BuildQuerySession(ctx, req, *summary, agentDef, sessionbuild.Options{
		Created:           false,
		Locale:            input.Locale,
		IncludeHistory:    false,
		IncludeMemory:     true,
		AllowInvokeAgents: sessionbuild.ResolvedModeCapabilities(agentDef).InvokeChildren,
	})
	if buildErr != nil {
		if statusErr := timeContractStatusError(buildErr); statusErr != nil {
			return preparedQuery{}, statusErr
		}
		return preparedQuery{}, btwStatusError(500, "btw_prepare_failed", buildErr.Error())
	}
	req.References = session.RuntimeContext.References
	history, loadErr := branch.LoadRawMessages(chat.DefaultHistoryRunWindow)
	if loadErr != nil {
		if statusErr := timeContractStatusError(loadErr); statusErr != nil {
			return preparedQuery{}, statusErr
		}
		return preparedQuery{}, btwStatusError(500, "btw_history_failed", loadErr.Error())
	}
	session.HistoryMessages = history
	ApplyQueryModelOptionsToSession(req.Model, &session)
	session.PlanningMode = false
	session.RunScopeID = "btw:" + input.ChatID + ":" + btwID
	session.SupportsContextCompaction = false
	session.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly
	session.RunLimits = contracts.RunLimits{
		MaxToolRounds:     3,
		MaxToolCalls:      4,
		FinalAnswerPrompt: btwFinalAnswerPrompt(s.deps.Config.Prompts.BTW),
	}
	modelReq := req
	modelReq.Message = BuildBTWUserMessage(s.deps.Config.Prompts.BTW, input.Message)
	session.CurrentMessages = s.deps.Sessions.BuildCurrentMessages(modelReq, session)

	systemInits, loadErr := branch.LoadAllSystemInits()
	if loadErr != nil {
		if statusErr := timeContractStatusError(loadErr); statusErr != nil {
			return preparedQuery{}, statusErr
		}
		return preparedQuery{}, btwStatusError(500, "btw_system_cache_failed", loadErr.Error())
	}
	pendingSystem, cacheErr := s.deps.Sessions.PrepareSystemInitCacheFrom(req, &session, systemInits)
	if cacheErr != nil {
		return preparedQuery{}, btwStatusError(500, "btw_system_cache_failed", cacheErr.Error())
	}
	summaryCopy := *summary
	summaryCopy.Usage = nil
	summaryCopy.PendingAwaiting = nil
	keepBranch = true
	keepRuntime = true
	return preparedQuery{
		Release:            releaseRuntime,
		Req:                req,
		Summary:            summaryCopy,
		Created:            false,
		AgentDef:           agentDef,
		TeamSnapshot:       teamSnapshot,
		Session:            session,
		MemoryUsageSummary: session.MemoryUsageSummary,
		SystemInitLine:     pendingSystem,
		ResourceBaseURL:    input.ResourceBaseURL,
		Execution: &queryExecutionOptions{
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
