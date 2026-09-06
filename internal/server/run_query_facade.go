package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/runtime/runstate"
	"agent-platform/internal/stream"
)

func (s *Server) StartRun(_ context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
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
	} else if _, ok := resolveCatalogTeam(s.deps.Registry, teamID); !ok {
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

	req := api.QueryRequest{
		ChatID:     chatID,
		AgentKey:   agentKey,
		TeamID:     teamID,
		Role:       api.QueryRoleUser,
		Message:    message,
		ChatSource: api.ChatSourceRunQueryPrefix + normalizeChatSourcePart(request.Origin.AgentKey),
	}
	ctx := s.backgroundCtx
	ctx = withChatSourceContext(ctx, req.ChatSource)
	if subject := strings.TrimSpace(request.Origin.Subject); subject != "" {
		ctx = WithPrincipal(ctx, &Principal{Subject: subject})
	}
	admission, err := s.prepareQueryAdmissionRequest(ctx, req, true, i18n.DefaultLocale, "")
	if err != nil {
		return contracts.RunSnapshot{}, mapRunAdmissionError(err, agentKey, teamID)
	}
	admission.strictOwner = true
	prepared, err := s.completeQueryPreparation(ctx, admission, nil)
	if err != nil {
		return contracts.RunSnapshot{}, mapRunAdmissionError(err, agentKey, teamID)
	}
	origin := request.Origin
	prepared.session.RunOrigin = &origin
	auditMetadata := map[string]any{
		"runOrigin": map[string]any{
			"agentKey": strings.TrimSpace(origin.AgentKey),
			"chatId":   strings.TrimSpace(origin.ChatID),
			"runId":    strings.TrimSpace(origin.RunID),
			"toolId":   strings.TrimSpace(origin.ToolID),
		},
	}
	prepared.req.TrustedQueryMetadata = contracts.CloneMap(auditMetadata)
	prepared.execution = &queryExecutionOptions{
		StepLineStore:   s.deps.Chats,
		CompletionStore: s.deps.Chats,
		QueryMetadata:   auditMetadata,
	}

	registered, statusErr := s.registerQueryRun(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		return contracts.RunSnapshot{}, mapRunStatusError(statusErr)
	}
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		return contracts.RunSnapshot{}, runToolError("internal_error", "run event bus unavailable")
	}

	if isProxyRoutedAgent(prepared.agentDef) {
		s.startPreparedProxyRun(prepared, registered, eventBus)
	} else {
		s.startPreparedLocalRun(prepared, registered, eventBus, PrincipalFromContext(ctx))
	}
	return s.GetRunStatus(prepared.req.RunID)
}

func (s *Server) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}

func (s *Server) InterruptRun(req api.InterruptRequest) (api.InterruptResponse, error) {
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return api.InterruptResponse{}, mapRunStatusError(statusErr)
	}
	if response, statusErr, forwarded := s.forwardProxyInterrupt(req); forwarded {
		if statusErr != nil {
			return api.InterruptResponse{}, mapRunStatusError(statusErr)
		}
		// Always cancel the local proxy bridge after forwarding so detached
		// upstream work cannot keep the platform run active indefinitely.
		s.deps.Runs.Interrupt(httpAPIUserInterruptRequest(req))
		return response, nil
	}
	ack := s.deps.Runs.Interrupt(httpAPIUserInterruptRequest(req))
	return api.InterruptResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		Detail:   ack.Detail,
	}, nil
}

func (s *Server) startPreparedProxyRun(prepared preparedQuery, registered registeredQueryRun, eventBus *stream.RunEventBus) {
	s.launchPreparedProxyRun(prepared, registered, eventBus, nil)
}

func (s *Server) startPreparedProxyRunAndWait(prepared preparedQuery, registered registeredQueryRun, eventBus *stream.RunEventBus) error {
	started := make(chan error, 1)
	s.launchPreparedProxyRun(prepared, registered, eventBus, started)
	return <-started
}

func (s *Server) launchPreparedProxyRun(prepared preparedQuery, registered registeredQueryRun, eventBus *stream.RunEventBus, started chan<- error) {
	s.broadcast("run.started", runStartedPushPayload(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey, registered.StartedAtMillis))
	route := newDetachedProxyRunRoute(prepared)
	s.registerProxyRun(route)

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.systemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, registered.StartedAtMillis, prepared.agentDef, s.deps.Chats, stepWriter, registered.Control, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
	proxyCtx, cancelProxy := context.WithCancel(registered.RunCtx)
	stopLifecycle := context.AfterFunc(s.backgroundCtx, cancelProxy)
	go func() {
		defer cancelProxy()
		defer stopLifecycle()
		s.runProxyWebSocketWithStartup(proxyCtx, prepared, route, eventBus, recorder, started)
	}()
}

func runOwnerMatchesChat(summary *chat.Summary, agentKey string, teamID string) bool {
	if summary == nil {
		return true
	}
	if teamID != "" {
		return strings.TrimSpace(summary.AgentKey) == "" && strings.TrimSpace(summary.TeamID) == teamID
	}
	return strings.TrimSpace(summary.TeamID) == "" && strings.TrimSpace(summary.AgentKey) == agentKey
}

func mapRunAdmissionError(err error, agentKey string, teamID string) error {
	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		return err
	}
	if strings.Contains(strings.ToLower(statusErr.message), "agent not found") && agentKey != "" {
		return runToolError("agent_not_found", statusErr.message)
	}
	if strings.Contains(strings.ToLower(statusErr.message), "team") && strings.Contains(strings.ToLower(statusErr.message), "not found") && teamID != "" {
		return runToolError("team_not_found", statusErr.message)
	}
	return mapRunStatusError(statusErr)
}

func mapRunStatusError(err *statusError) error {
	if err == nil {
		return nil
	}
	code := strings.TrimSpace(err.code)
	if code == "" {
		switch err.status {
		case http.StatusNotFound:
			code = "run_not_found"
		case http.StatusForbidden:
			code = "run_not_owned"
		default:
			code = "invalid_request"
		}
	}
	return runToolError(code, err.message)
}

func runToolError(code string, message string) error {
	return &contracts.RunToolError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}

func cloneRunOrigin(origin *contracts.RunOrigin) *contracts.RunOrigin {
	return runstate.CloneRunOrigin(origin)
}
