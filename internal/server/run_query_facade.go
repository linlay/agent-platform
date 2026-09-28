package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

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
	s.broadcast("run.started", runStartedPushPayload(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey, registered.StartedAtMillis))
	route := newDetachedProxyRunRoute(prepared)
	s.registerProxyRun(route)

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.Req.ChatID, prepared.Req.RunID, prepared.AgentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.SystemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.Session.CurrentMessages)
	var chatUsage chat.UsageData
	if prepared.Summary.Usage != nil {
		chatUsage = *prepared.Summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.Req, registered.StartedAtMillis, prepared.AgentDef, s.deps.Chats, stepWriter, registered.Control, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
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
	if strings.Contains(strings.ToLower(statusErr.Message), "agent not found") && agentKey != "" {
		return runToolError("agent_not_found", statusErr.Message)
	}
	if strings.Contains(strings.ToLower(statusErr.Message), "team") && strings.Contains(strings.ToLower(statusErr.Message), "not found") && teamID != "" {
		return runToolError("team_not_found", statusErr.Message)
	}
	return mapRunStatusError(statusErr)
}

func mapRunStatusError(err *statusError) error {
	if err == nil {
		return nil
	}
	code := strings.TrimSpace(err.Code)
	if code == "" {
		switch err.Status {
		case http.StatusNotFound:
			code = "run_not_found"
		case http.StatusForbidden:
			code = "run_not_owned"
		default:
			code = "invalid_request"
		}
	}
	return runToolError(code, err.Message)
}

func runToolError(code string, message string) error {
	return &contracts.RunToolError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
