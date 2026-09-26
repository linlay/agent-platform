package catalog

import (
	"fmt"
	"strings"
)

// ResolveContextAgentKeys selects prompt candidates, never execution grants or
// required dependencies. available contains keys from the effective catalog.
// The diagnostic deliberately does not copy target configuration or load errors.
func ResolveContextAgentKeys(def AgentDefinition, currentKey string, available []string) ([]string, *AdminAgentDiagnostic) {
	enabled := false
	for _, tag := range def.ContextTags {
		if strings.EqualFold(strings.TrimSpace(tag), "agents") {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, nil
	}
	currentKey = strings.TrimSpace(currentKey)
	if currentKey == "" {
		currentKey = strings.TrimSpace(def.Key)
	}
	known := make(map[string]bool, len(available))
	for _, key := range available {
		known[key] = true
	}
	candidates := def.ContextAgents
	if len(candidates) == 0 {
		candidates = available
	}
	var selected, sample []string
	skipped := 0
	seen := make(map[string]bool, len(candidates))
	for _, key := range candidates {
		key = strings.TrimSpace(key)
		if key == "" || key == currentKey || seen[key] {
			continue
		}
		seen[key] = true
		if known[key] {
			selected = append(selected, key)
			continue
		}
		skipped++
		if len(sample) < 8 {
			// Bound each sample as well as its count; %q below escapes controls.
			runes := []rune(key)
			if len(runes) > 128 {
				key = string(runes[:128]) + "…"
			}
			sample = append(sample, key)
		}
	}
	if skipped == 0 {
		return selected, nil
	}
	return selected, &AdminAgentDiagnostic{
		Severity: "warning",
		Code:     "context_agents_unavailable",
		Message:  fmt.Sprintf("contextConfig.agents: skipped %d unavailable prompt candidate(s); sample=%q; missing or invalid agents are omitted, query may continue; inspect target Agent diagnostics", skipped, sample),
	}
}

// Resolve warnings while holding the registry read lock so target removals,
// repairs and runtime invalidation are reflected without caching stale warnings.
func (r *FileRegistry) adminAgentWithContextDiagnosticsLocked(item AdminAgent) AdminAgent {
	item = cloneAdminAgent(item)
	def, ok := r.agents[item.Key]
	if !ok || len(def.ContextAgents) == 0 {
		return item
	}
	keys := make([]string, 0, len(def.ContextAgents))
	for _, key := range def.ContextAgents {
		if _, available := r.agents[strings.TrimSpace(key)]; available {
			keys = append(keys, strings.TrimSpace(key))
		}
	}
	_, diagnostic := ResolveContextAgentKeys(def, def.Key, keys)
	if diagnostic != nil {
		diagnostic.SourcePath = item.Source.Path
		item.Diagnostics = append(item.Diagnostics, *diagnostic)
	}
	return item
}
