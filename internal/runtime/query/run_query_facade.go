package query

import (
	"context"
	"errors"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/i18n"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/runstate"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

func runOwnerMatchesChat(summary *chat.Summary, agentKey string, teamID string) bool {
	if summary == nil {
		return true
	}
	if teamID != "" {
		return strings.TrimSpace(summary.AgentKey) == "" && strings.TrimSpace(summary.TeamID) == teamID
	}
	return strings.TrimSpace(summary.TeamID) == "" && strings.TrimSpace(summary.AgentKey) == agentKey
}

func mapRunStatusError(err *statusError) error {
	if err == nil {
		return nil
	}
	code := strings.TrimSpace(err.Code)
	if code == "" {
		switch err.Status {
		case 404:
			code = "run_not_found"
		case 403:
			code = "run_not_owned"
		default:
			code = "invalid_request"
		}
	}
	return runToolError(code, err.Message)
}

func (s *Service) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}

func (s *Service) StartRun(_ context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	accessLevel, valid := contracts.NormalizeAccessLevel(request.AccessLevel)
	if !valid {
		return contracts.RunSnapshot{}, runToolError("invalid_request", "accessLevel must be default, auto_approve, or full_access")
	}
	if accessLevel != contracts.AccessLevelDefault && !s.deps.Config.RunQuery.AllowAccessLevelOverride {
		return contracts.RunSnapshot{}, runToolError("run_access_level_override_disabled", "runQuery.allowAccessLevelOverride is disabled")
	}
	if strings.TrimSpace(request.AccessLevel) == "" {
		// Inheritance is not an explicit override. Read the live control state so
		// changes made after the parent session started apply to new runs too.
		if parent, ok := s.deps.Runs.RunStatus(strings.TrimSpace(request.Origin.RunID)); ok {
			accessLevel = sessionbuild.NormalizedAccessLevel(parent.AccessLevel)
		}
	}
	chatName := strings.TrimSpace(request.ChatName)
	if chatName != "" && strings.TrimSpace(request.ChatID) != "" {
		return contracts.RunSnapshot{}, runToolError("invalid_request", "chatName cannot be combined with chatId")
	}
	agentKey := strings.TrimSpace(request.AgentKey)
	teamID := strings.TrimSpace(request.TeamID)
	message := strings.TrimSpace(request.Message)
	if message == "" || (agentKey == "") == (teamID == "") {
		return contracts.RunSnapshot{}, runToolError("invalid_request", "message and exactly one of agentKey or teamId are required")
	}
	if agentKey != "" {
		if _, ok := s.deps.Registry.AgentDefinition(agentKey); !ok {
			return contracts.RunSnapshot{}, runToolError("agent_not_found", "agent not found")
		}
	} else if _, ok := catalogview.ResolveTeam(s.deps.Registry, teamID); !ok {
		return contracts.RunSnapshot{}, runToolError("team_not_found", "team not found")
	}

	chatID := strings.TrimSpace(request.ChatID)
	if chatID != "" {
		summary, err := s.deps.Chats.Summary(chatID)
		if err != nil && !errors.Is(err, chat.ErrChatNotFound) {
			return contracts.RunSnapshot{}, err
		}
		if summary != nil && !runOwnerMatchesChat(summary, agentKey, teamID) {
			return contracts.RunSnapshot{}, runToolError("target_owner_mismatch", "target identity does not match chat owner")
		}
	}

	req := runtimetypes.QueryCommand{
		ChatID:          chatID,
		AgentKey:        agentKey,
		TeamID:          teamID,
		Role:            queryinput.QueryRoleUser,
		Message:         message,
		AccessLevel:     accessLevel,
		MustUseSkills:   append([]string(nil), request.MustUseSkills...),
		InitialChatName: chatName,
		ChatSource:      queryinput.ChatSourceRunQueryPrefix + normalizeChatSourcePart(request.Origin.AgentKey),
	}
	ctx := s.backgroundCtx
	// Detach execution lifetime, but retain the trusted parent's connection scope.
	// RunOrigin describes derivation separately from transport/lane.
	parentRunID := strings.TrimSpace(request.Origin.RunID)
	if parentRunID == "" {
		return contracts.RunSnapshot{}, runToolError("run_context_required", "run_query requires a parent runId")
	}
	scope, err := s.runControlScopes().Load(parentRunID)
	if err != nil || (scope.Transport != "http" && scope.Transport != "ws") {
		return contracts.RunSnapshot{}, runToolError("run_control_identity_unavailable", "cannot inherit parent run transport")
	}
	ctx = controlscope.WithContext(ctx, scope)
	ctx = runtimetypes.WithChatSource(ctx, req.ChatSource)
	if subject := strings.TrimSpace(request.Origin.Subject); subject != "" {
		ctx = runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: subject})
	}
	admission, err := s.PrepareQueryAdmissionRequest(ctx, req, true, i18n.DefaultLocale, "")
	if err != nil {
		return contracts.RunSnapshot{}, mapRunAdmissionError(err, agentKey, teamID)
	}
	admission.StrictOwner = true
	prepared, err := s.CompleteQueryPreparation(ctx, admission, nil)
	if err != nil {
		return contracts.RunSnapshot{}, mapRunAdmissionError(err, agentKey, teamID)
	}
	origin := request.Origin
	prepared.Session.RunOrigin = &origin
	auditMetadata := map[string]any{
		"runOrigin": map[string]any{
			"agentKey": strings.TrimSpace(origin.AgentKey),
			"chatId":   strings.TrimSpace(origin.ChatID),
			"runId":    strings.TrimSpace(origin.RunID),
			"toolId":   strings.TrimSpace(origin.ToolID),
		},
	}
	prepared.Req.TrustedQueryMetadata = contracts.CloneMap(auditMetadata)
	prepared.Execution = &queryExecutionOptions{
		StepLineStore:   s.deps.Chats,
		CompletionStore: s.deps.Chats,
		QueryMetadata:   auditMetadata,
	}

	registered, statusErr := s.RegisterPreparedQuery(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		return contracts.RunSnapshot{}, mapRunStatusError(statusErr)
	}
	eventBus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
	if !ok {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.FinishRegisteredQuery(prepared, registered)
		return contracts.RunSnapshot{}, runToolError("internal_error", "run event bus unavailable")
	}

	if sessionbuild.IsProxyRoutedAgent(prepared.AgentDef) {
		s.deps.Proxy.Start(prepared, registered, eventBus, false)
	} else {
		s.startPreparedLocalRun(prepared, registered, eventBus)
	}
	return s.GetRunStatus(prepared.Req.RunID)
}

func mapRunAdmissionError(err error, agentKey string, teamID string) error {
	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		return err
	}
	if strings.Contains(strings.ToLower(statusErr.Message), "agent not found") && agentKey != "" {
		return runToolError("agent_not_found", statusErr.Message)
	}
	if strings.Contains(strings.ToLower(statusErr.Message), "team") && strings.Contains(strings.ToLower(statusErr.Message), "not found") && teamID != "" {
		return runToolError("team_not_found", statusErr.Message)
	}
	return mapRunStatusError(statusErr)
}

func runToolError(code string, message string) error {
	return &contracts.RunToolError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
