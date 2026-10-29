package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func (s *Server) InterruptRun(req api.InterruptRequest) (api.InterruptResponse, error) {
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey); statusErr != nil {
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
