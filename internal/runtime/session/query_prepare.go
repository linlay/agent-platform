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

func ResolveSkillRuntimeSettings(agentEnv map[string]string, agentDir string, centerDir string, skillKeys []string, agents ...catalog.AgentDefinition) ([]string, map[string]string, error) {
	_ = centerDir
	runtimeEnv := contracts.CloneStringMap(agentEnv)
	if err := agentconfig.ValidateUserEnvironment(runtimeEnv); err != nil {
		return nil, nil, err
	}
	if len(skillKeys) == 0 {
		return nil, runtimeEnv, nil
	}
	seen := map[string]struct{}{}
	var hookDirs []string
	for _, raw := range skillKeys {
		skillKey := strings.ToLower(strings.TrimSpace(raw))
		if skillKey == "" {
			continue
		}
		if _, ok := seen[skillKey]; ok {
			continue
		}
		seen[skillKey] = struct{}{}
		agent := catalog.AgentDefinition{RuntimeDir: agentDir}
		if len(agents) > 0 {
			agent = agents[0]
		}
		def, ok, err := agent.ResolveSkillDefinition(skillKey)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve skill runtime %q: %w", skillKey, err)
		}
		if !ok {
			log.Printf("[server][skill-runtime][warn] skill definition not found key=%s", skillKey)
			continue
		}
		if strings.TrimSpace(def.BashHooksDir) != "" {
			hookDirs = append(hookDirs, def.BashHooksDir)
		}
		for key, value := range def.RuntimeEnv {
			if runtimeEnv == nil {
				runtimeEnv = make(map[string]string, len(agentEnv)+len(def.RuntimeEnv))
			}
			runtimeEnv[key] = value
		}
	}
	return hookDirs, runtimeEnv, nil
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
