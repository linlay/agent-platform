package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
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
