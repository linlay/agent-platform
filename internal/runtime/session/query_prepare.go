package session

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

func RuntimeAgentEnv(value any) map[string]string {
	switch env := value.(type) {
	case map[string]string:
		return contracts.CloneStringMap(env)
	default:
		return nil
	}
}

func ResolveSkillRuntimeSettings(agentEnv map[string]string, agentDir string, centerDir string, skillIDs []string, agents ...catalog.AgentDefinition) (map[string]string, error) {
	_ = centerDir
	runtimeEnv := contracts.CloneStringMap(agentEnv)
	if err := agentconfig.ValidateUserEnvironment(runtimeEnv); err != nil {
		return nil, err
	}
	if len(skillIDs) == 0 {
		return runtimeEnv, nil
	}
	seen := map[string]struct{}{}
	for _, raw := range skillIDs {
		skillID := strings.ToLower(strings.TrimSpace(raw))
		if skillID == "" {
			continue
		}
		if _, ok := seen[skillID]; ok {
			continue
		}
		seen[skillID] = struct{}{}
		agent := catalog.AgentDefinition{RuntimeDir: agentDir}
		if len(agents) > 0 {
			agent = agents[0]
		}
		def, ok, err := agent.ResolveSkillDefinition(skillID)
		if err != nil {
			return nil, fmt.Errorf("resolve skill runtime %q: %w", skillID, err)
		}
		if !ok {
			log.Printf("[server][skill-runtime][warn] skill definition not found id=%s", skillID)
			continue
		}
		for key, value := range def.RuntimeEnv {
			if runtimeEnv == nil {
				runtimeEnv = make(map[string]string, len(agentEnv)+len(def.RuntimeEnv))
			}
			runtimeEnv[key] = value
		}
	}
	return runtimeEnv, nil
}

func SortedStringKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
