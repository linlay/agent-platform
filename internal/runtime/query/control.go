package query

import (
	"context"
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Service) Submit(_ context.Context, command runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	if strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.AwaitingID) == "" {
		return runtimetypes.SubmitResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId and awaitingId are required")
	}

	req := queryinput.SubmitRequest{
		ChatID: command.ChatID, RunID: command.RunID, AgentKey: command.AgentKey, TeamID: command.TeamID,
		AwaitingID: command.AwaitingID, SubmitID: command.SubmitID, Locale: command.Locale,
		Param: queryinput.SubmitParam(command.Param), Params: queryinput.SubmitParams(command.Params), ContinuationRunID: command.ContinuationRunID,
		ContinuationState: command.ContinuationState,
	}
	req = s.normalizeActiveSubmitRun(req)
	if statusErr := s.ValidateSubmitOwner(req); statusErr != nil {
		return runtimetypes.SubmitResult{}, statusErr
	}
	if response, statusErr, ok := s.deps.Proxy.Submit(req); ok {
		if statusErr != nil {
			return runtimetypes.SubmitResult{}, statusErr
		}
		return submitResultToRuntime(response), nil
	}
	response, _, _, err := s.resolveSubmit(req)
	if err != nil {
		return runtimetypes.SubmitResult{}, err
	}
	return submitResultToRuntime(response), nil
}

func (s *Service) Steer(_ context.Context, command runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	if strings.TrimSpace(command.RunID) == "" {
		return runtimetypes.SteerResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId is required")
	}

	req := queryinput.SteerRequest{
		RequestID: command.RequestID, ChatID: command.ChatID, RunID: command.RunID,
		SteerID: command.SteerID, AgentKey: command.AgentKey, TeamID: command.TeamID, Message: command.Message,
		References: command.References,
	}
	if statusErr := s.ValidateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return runtimetypes.SteerResult{}, statusErr
	}
	status, _ := s.deps.Runs.RunStatus(req.RunID)
	if req.ChatID != "" && req.ChatID != status.ChatID {
		return runtimetypes.SteerResult{}, &statusError{Status: 400, Message: "chatId does not match run"}
	}
	req.ChatID = status.ChatID
	if response, statusErr, ok := s.deps.Proxy.Steer(req); ok {
		if statusErr != nil {
			return runtimetypes.SteerResult{}, statusErr
		}
		return runtimetypes.SteerResult{Accepted: response.Accepted, Status: response.Status, RunID: response.RunID, SteerID: response.SteerID, Detail: response.Detail}, nil
	}
	ack := s.deps.Runs.Steer(req)
	return runtimetypes.SteerResult{Accepted: ack.Accepted, Status: ack.Status, RunID: req.RunID, SteerID: ack.SteerID, Detail: ack.Detail}, nil
}

func (s *Service) Interrupt(_ context.Context, command runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	if strings.TrimSpace(command.RunID) == "" {
		return runtimetypes.InterruptResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId is required")
	}

	req := queryinput.InterruptRequest{
		RequestID: command.RequestID, ChatID: command.ChatID, RunID: command.RunID,
		AgentKey: command.AgentKey, TeamID: command.TeamID, Message: command.Message,
		InterruptSource: command.Source, InterruptReason: command.Reason, InterruptDetail: command.Detail,
	}
	if statusErr := s.ValidateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return runtimetypes.InterruptResult{}, statusErr
	}
	if response, statusErr, forwarded := s.deps.Proxy.Interrupt(req); forwarded {
		if statusErr != nil {
			return runtimetypes.InterruptResult{}, statusErr
		}
		s.deps.Runs.Interrupt(runtimeLocalInterruptRequest(command, req))
		if command.Caller.Scope == "server" {
			s.deps.Runs.Finish(req.RunID)
		}
		return runtimetypes.InterruptResult{Accepted: response.Accepted, Status: response.Status, RunID: response.RunID, Detail: response.Detail}, nil
	}
	ack := s.deps.Runs.Interrupt(runtimeLocalInterruptRequest(command, req))
	if command.Caller.Scope == "server" {
		s.deps.Runs.Finish(req.RunID)
	}
	return runtimetypes.InterruptResult{Accepted: ack.Accepted, Status: ack.Status, RunID: req.RunID, Detail: ack.Detail}, nil
}

func (s *Service) SetAccessLevel(_ context.Context, command runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	if strings.TrimSpace(command.RunID) == "" {
		return runtimetypes.AccessLevelResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId is required")
	}

	response, statusErr := s.updateAccessLevel(queryinput.AccessLevelRequest{
		RequestID: command.RequestID, RunID: command.RunID, AgentKey: command.AgentKey,
		TeamID: command.TeamID, AccessLevel: command.AccessLevel, Reason: command.Reason,
	})
	if statusErr != nil {
		return runtimetypes.AccessLevelResult{}, statusErr
	}
	return runtimetypes.AccessLevelResult{
		Accepted: response.Accepted, Status: response.Status, RunID: response.RunID,
		PreviousAccessLevel: response.PreviousAccessLevel, AccessLevel: response.AccessLevel,
		Version: response.Version, Detail: response.Detail,
	}, nil
}

func runtimeLocalInterruptRequest(command runtimetypes.InterruptCommand, req queryinput.InterruptRequest) queryinput.InterruptRequest {
	if command.Caller.Scope == "server" {
		return interruptRequestWithCause(req, command.Source, command.Reason, command.Detail)
	}
	return httpAPIUserInterruptRequest(req)
}

func submitResultToRuntime(response queryinput.SubmitResponse) runtimetypes.SubmitResult {
	return runtimetypes.SubmitResult{
		Accepted: response.Accepted, Status: response.Status, ChatID: response.ChatID, RunID: response.RunID,
		AwaitingID: response.AwaitingID, SubmitID: response.SubmitID, Continued: response.Continued,
		ErrorCode: response.ErrorCode, Detail: response.Detail,
	}
}
