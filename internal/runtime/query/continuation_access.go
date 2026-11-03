package query

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func (s *Service) continuationAccess(original *chat.QueryLine, sourceRunID string, def catalog.AgentDefinition, newExecution bool) (string, string, string) {
	previous, requested := contracts.AccessLevelAskForApproval, ""
	if original != nil {
		previous = contracts.StoredAccessLevel(original.AccessLevel)
		requested, _ = original.Query["accessLevel"].(string)
		if original.LegacyAccessLevel && strings.TrimSpace(requested) != "" && contracts.IsDefaultAccessLevelRequest(requested) {
			requested = contracts.AccessLevelAskForApproval
		}
	}
	if status, ok := s.deps.Runs.RunStatus(sourceRunID); ok {
		previous = sessionbuildAccessLevel(status.AccessLevel)
		if !newExecution || status.AccessLevelVersion > 1 {
			return previous, "continuation", previous
		}
	}
	if !newExecution {
		return previous, "continuation", previous
	}
	level, source, _ := contracts.ResolveInitialAccessLevel(requested, def.AccessConfig)
	return level, source, previous
}

func sessionbuildAccessLevel(level string) string {
	normalized, _ := contracts.NormalizeAccessLevel(level)
	return normalized
}

// Freeze the execution permission before consuming a planning answer. A changed
// default may not silently raise the permission the user saw when planning.
func (s *Service) preparePlanExecutionAccess(req queryinput.SubmitRequest, admission *awaitingContinuationAdmission) error {
	original, err := s.deps.Chats.LoadRunQuery(req.ChatID, req.RunID)
	if err != nil {
		return err
	}
	level, source, previous := s.continuationAccess(original, req.RunID, admission.AgentDef, true)
	confirmed, _ := req.Param["executionAccessLevel"].(string)
	if contracts.AccessLevelRank(level) > contracts.AccessLevelRank(previous) && strings.TrimSpace(confirmed) != level {
		return &statusError{Status: 409, Code: "plan_access_level_changed", Message: "Agent default permission increased; review the current executionAccessLevel before approving", Data: map[string]any{"executionAccessLevel": level}}
	}
	admission.AccessLevel, admission.AccessSource = level, source
	admission.Frozen = true
	return nil
}
