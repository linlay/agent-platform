package query

import (
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
)

func (s *Service) updateAccessLevel(req queryinput.AccessLevelRequest) (queryinput.AccessLevelResponse, *statusError) {
	if strings.TrimSpace(req.RunID) == "" {
		return queryinput.AccessLevelResponse{}, &statusError{Status: 400, Message: "runId is required"}
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !ok {
		return queryinput.AccessLevelResponse{}, &statusError{Status: 400, Message: "accessLevel must be default, auto_approve, or full_access"}
	}
	req.AccessLevel = accessLevel
	if statusErr := s.ValidateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return queryinput.AccessLevelResponse{}, statusErr
	}
	// Validate before forwarding: ACP must never receive a disallowed change.
	if reader, ok := s.deps.Runs.(interface {
		RunInteractionConfig(string) (interaction.Config, bool)
	}); ok {
		if config, found := reader.RunInteractionConfig(req.RunID); found && !config.AccessLevel {
			return queryinput.AccessLevelResponse{RunID: req.RunID, Status: "interaction_disabled", Detail: "interactionConfig.accessLevel is disabled"}, nil
		}
	}
	if response, statusErr, ok := s.deps.Proxy.AccessLevel(req); ok {
		return response, statusErr
	}
	ack := s.deps.Runs.UpdateAccessLevel(req)
	return queryinput.AccessLevelResponse{
		Accepted:            ack.Accepted,
		Status:              ack.Status,
		RunID:               req.RunID,
		PreviousAccessLevel: ack.PreviousAccessLevel,
		AccessLevel:         ack.AccessLevel,
		Version:             ack.Version,
		Detail:              ack.Detail,
	}, nil
}
