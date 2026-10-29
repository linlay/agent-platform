package session

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

// ResolveQueryAccessLevel also serves internal builders that bypass admission.
func ResolveQueryAccessLevel(req runtimetypes.QueryCommand, def catalog.AgentDefinition) string {
	if req.Origin != nil && req.Origin.Type == queryinput.OriginAutomation && contracts.IsDefaultAccessLevelRequest(req.AccessLevel) {
		return contracts.AccessLevelAskForApproval
	}
	level, _, err := contracts.ResolveInitialAccessLevel(req.AccessLevel, def.AccessConfig)
	if err != nil {
		return contracts.AccessLevelAskForApproval
	}
	return level
}
