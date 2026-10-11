package catalog

import (
	agentteam "agent-platform/internal/agent/team"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func parseTeamConfig(mode, key string, raw any) (*TeamConfig, error) {
	if mode != "TEAM" {
		if raw != nil {
			return nil, fmt.Errorf("teamConfig is only supported for TEAM")
		}
		return nil, nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("teamConfig.members is required")
	}
	for field := range node {
		if field != "members" && field != "maxParallel" {
			return nil, fmt.Errorf("unknown teamConfig field %q", field)
		}
	}
	values, ok := node["members"].([]any)
	if typed, yes := node["members"].([]string); yes {
		ok = true
		values = make([]any, len(typed))
		for i, v := range typed {
			values[i] = v
		}
	}
	if !ok || len(values) == 0 {
		return nil, fmt.Errorf("teamConfig.members must be a non-empty array")
	}
	c := &TeamConfig{MaxParallel: agentteam.DefaultMaxParallel}
	seen := map[string]bool{}
	for _, raw := range values {
		member, ok := raw.(string)
		member = strings.TrimSpace(member)
		if !ok || member == "" || strings.EqualFold(member, key) || seen[strings.ToLower(member)] {
			return nil, fmt.Errorf("invalid, duplicate or self-referencing team member %q", member)
		}
		seen[strings.ToLower(member)] = true
		c.Members = append(c.Members, member)
	}
	if value, exists := node["maxParallel"]; exists {
		switch v := value.(type) {
		case int:
			c.MaxParallel = v
		case int64:
			c.MaxParallel = int(v)
		case float64:
			if v != float64(int(v)) {
				return nil, fmt.Errorf("teamConfig.maxParallel must be an integer")
			}
			c.MaxParallel = int(v)
		default:
			return nil, fmt.Errorf("teamConfig.maxParallel must be an integer")
		}
		if c.MaxParallel < 1 || c.MaxParallel > agentteam.MaxParallel {
			return nil, fmt.Errorf("teamConfig.maxParallel must be between 1 and 5")
		}
	}
	return c, nil
}

func validateTeamTools(def AgentDefinition) error {
	if def.Mode == "TEAM" {
		for _, name := range def.Tools {
			if name == "agent_invoke" {
				return fmt.Errorf("TEAM effective tools cannot contain agent_invoke")
			}
		}
	}
	return nil
}

func teamRosterFingerprint(snapshot TeamSnapshot) string {
	type rosterMember struct {
		Key         string `json:"key"`
		Name        string `json:"name,omitempty"`
		Role        string `json:"role,omitempty"`
		Description string `json:"description,omitempty"`
		Available   bool   `json:"available"`
	}
	members := make([]rosterMember, 0, len(snapshot.AgentKeys))
	for _, key := range snapshot.AgentKeys {
		member := rosterMember{Key: key}
		if def, ok := snapshot.agentDefinitions[key]; ok {
			member.Available = true
			member.Name = strings.TrimSpace(def.Name)
			member.Role = strings.TrimSpace(def.Role)
			member.Description = strings.TrimSpace(def.Description)
		}
		members = append(members, member)
	}
	return teamCatalogFingerprint(struct {
		Members []rosterMember `json:"members"`
	}{Members: members})
}

func teamOrchestratorFingerprint(snapshot TeamSnapshot) string {
	return teamCatalogFingerprint(struct {
		AgentKey          string                 `json:"agentKey"`
		RuntimeMode       string                 `json:"runtimeMode"`
		AgentKeys         []string               `json:"agentKeys"`
		ToolSchemaVersion string                 `json:"toolSchemaVersion"`
		Orchestrator      TeamOrchestratorConfig `json:"orchestrator"`
		SoulPrompt        string                 `json:"soulPrompt"`
		AgentsPrompt      string                 `json:"agentsPrompt"`
	}{
		AgentKey:          snapshot.AgentKey,
		RuntimeMode:       snapshot.RuntimeMode,
		AgentKeys:         append([]string(nil), snapshot.AgentKeys...),
		ToolSchemaVersion: agentteam.HiddenToolSchemaVersion,
		Orchestrator:      cloneTeamOrchestratorConfig(snapshot.Orchestrator),
		SoulPrompt:        snapshot.SoulPrompt,
		AgentsPrompt:      snapshot.AgentsPrompt,
	})
}

func teamHiddenToolSchemaFingerprint(snapshot TeamSnapshot) string {
	return teamCatalogFingerprint(struct {
		SchemaVersion string   `json:"schemaVersion"`
		AgentKeys     []string `json:"agentKeys"`
	}{
		SchemaVersion: agentteam.HiddenToolSchemaVersion,
		AgentKeys:     append([]string(nil), snapshot.ValidAgentKeys...),
	})
}

func teamCatalogFingerprint(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
