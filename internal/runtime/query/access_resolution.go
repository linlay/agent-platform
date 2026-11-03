package query

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

// ResolveQueryAccess records a platform decision separately from OriginalQuery.
// AccessGrant is trusted internal input and is never decoded from the wire.
func ResolveQueryAccess(req *runtimetypes.QueryCommand, def catalog.AgentDefinition) {
	if req.AccessGrant != nil && strings.TrimSpace(req.AccessLevel) != "" {
		req.AccessLevel, _ = contracts.NormalizeAccessLevel(contracts.StoredAccessLevel(req.AccessLevel))
		return
	}
	level, source, _ := contracts.ResolveInitialAccessLevel(req.AccessLevel, def.AccessConfig)
	if req.Origin != nil && req.Origin.Type == queryinput.OriginAutomation {
		source = "automation_config"
		if contracts.IsDefaultAccessLevelRequest(req.AccessLevel) {
			level, source = contracts.AccessLevelAskForApproval, "automation_default"
		}
	}
	req.AccessLevel = level
	req.AccessGrant = map[string]any{"source": source, "accessLevel": level, "agentKey": def.Key, "agentRevision": def.RuntimeRevision}
}
