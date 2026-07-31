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
	runID = strings.TrimSpace(runID)
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		return contracts.RunSnapshot{}, runToolError("run_not_found", "run not found")
	}
	snapshot := contracts.RunSnapshot{
		RunID:       status.RunID,
		ChatID:      status.ChatID,
		AgentKey:    status.AgentKey,
		TeamID:      status.TeamID,
		Status:      runPublicStatus(status.State),
		LastSeq:     status.LastSeq,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Origin:      cloneRunOrigin(status.RunOrigin),
	}
	if status.State == contracts.RunLoopStateWaitingSubmit {
		if lister, ok := s.deps.Runs.(contracts.ActiveAwaitingLister); ok {
			for _, awaiting := range lister.ActiveAwaitings(runID) {
				if !strings.EqualFold(strings.TrimSpace(awaiting.Mode), "question") {
					continue
				}
				publicID := strings.TrimSpace(awaiting.PublicAwaitingID)
				if publicID == "" {
					publicID = strings.TrimSpace(awaiting.AwaitingID)
				}
				snapshot.Awaiting = &contracts.RunAwaiting{
					AwaitingID: publicID,
					Mode:       "question",
					Questions:  append([]any(nil), awaiting.Questions...),
				}
				break
			}
		}
	}
	if eventBus, exists := s.deps.Runs.EventBus(runID); exists {
		applyRunEventSnapshot(&snapshot, eventBus.Snapshot())
	}
	if snapshot.Status == "completed" && s.deps.Chats != nil {
		if summary, err := s.deps.Chats.Summary(snapshot.ChatID); err == nil && summary != nil && strings.TrimSpace(summary.LastRunID) == runID {
			snapshot.Content = summary.LastRunContent
		}
	}
	return snapshot, nil
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
	go s.runProxyWebSocket(registered.RunCtx, prepared, route, eventBus, recorder)
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

func runPublicStatus(state contracts.RunLoopState) string {
	switch state {
	case contracts.RunLoopStateWaitingSubmit:
		return "awaiting"
	case contracts.RunLoopStateCompleted:
		return "completed"
	case contracts.RunLoopStateFailed:
		return "failed"
	case contracts.RunLoopStateCancelled:
		return "interrupted"
	default:
		return "running"
	}
}

func applyRunEventSnapshot(snapshot *contracts.RunSnapshot, events []stream.EventData) {
	if snapshot == nil {
		return
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		switch event.Type {
		case "content.snapshot":
			if snapshot.Content == "" && strings.TrimSpace(event.String("taskId")) == "" {
				snapshot.Content = event.String("text")
			}
		case "awaiting.ask":
			if snapshot.Awaiting != nil && snapshot.Awaiting.Payload == nil && event.String("awaitingId") == snapshot.Awaiting.AwaitingID {
				snapshot.Awaiting.Payload = contracts.CloneMap(event.Payload)
			}
		case "run.error":
			if snapshot.Error == nil {
				if payload, ok := event.Payload["error"].(map[string]any); ok {
					snapshot.Error = contracts.CloneMap(payload)
				} else {
					snapshot.Error = contracts.CloneMap(event.Payload)
				}
			}
		case "run.cancel":
			if snapshot.Error == nil {
				snapshot.Error = contracts.CloneMap(event.Payload)
			}
		}
	}
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
	if origin == nil {
		return nil
	}
	cloned := *origin
	return &cloned
}
