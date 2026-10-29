package session

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

// ResolveQueryAccessLevel also serves internal builders that bypass admission.
func ResolveQueryAccessLevel(req runtimetypes.QueryCommand, def catalog.AgentDefinition) string {
	if req.Origin != nil && req.Origin.Type == queryinput.OriginAutomation && strings.TrimSpace(req.AccessLevel) == "" {
		return contracts.AccessLevelDefault
	}
	level, _, err := contracts.ResolveInitialAccessLevel(req.AccessLevel, def.AccessConfig)
	if err != nil {
		return contracts.AccessLevelDefault
	}
	return level
}
