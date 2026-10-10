package catalogview

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"fmt"
	"strings"
)

func ResolveAgentTarget(registry catalog.Registry, agentKey string, existing *chat.Summary) (string, *catalog.TeamSnapshot, *runtimetypes.RequestError) {
	agentKey = strings.TrimSpace(agentKey)
	if existing != nil && (existing.AgentKey != "" || existing.LastRunID != "") {
		if agentKey != "" && agentKey != existing.AgentKey {
			return "", nil, &runtimetypes.RequestError{Status: 409, Code: "target_owner_mismatch", Message: "agentKey does not match chat owner"}
		}
		agentKey = existing.AgentKey
	}
	if agentKey == "" {
		agentKey = registry.DefaultAgentKey()
	}
	def, ok := registry.AgentDefinition(agentKey)
	if !ok || def.Mode != "TEAM" {
		return agentKey, nil, nil
	}
	snapshot, ok := ResolveTeam(registry, agentKey)
	if !ok {
		return "", nil, &runtimetypes.RequestError{Status: 503, Code: "unavailable", Message: "TEAM runtime is unavailable"}
	}
	if err := ValidateTeamSnapshot(snapshot); err != nil {
		return "", nil, err
	}
	return agentKey, &snapshot, nil
}
func ValidateTeamSnapshot(snapshot catalog.TeamSnapshot) *runtimetypes.RequestError {
	unavailable := append([]string(nil), snapshot.InvalidAgentKeys...)
	for _, key := range snapshot.ValidAgentKeys {
		def, exists := snapshot.AgentDefinition(key)
		invalid := !exists || def.Mode == "TEAM" || def.Engine == "acp" || catalog.AgentUsesACPCoderBackend(def) || !sessionbuild.ResolvedModeCapabilities(def).RunAsChild
		for _, tool := range def.Tools {
			if tool == "agent_invoke" {
				invalid = true
			}
		}
		if invalid {
			unavailable = append(unavailable, key)
		}
	}
	if len(snapshot.AgentKeys) == 0 || len(unavailable) > 0 {
		return &runtimetypes.RequestError{Status: 503, Code: "unavailable", Message: fmt.Sprintf("TEAM Agent %q has unavailable members: %v", snapshot.AgentKey, unavailable)}
	}
	return nil
}
