package catalog

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// ConnectorUsers includes current source references (even for invalid or not yet
// reloaded Agents) and published runtimes retained by Run/Terminal leases.
func (r *FileRegistry) ConnectorUsers(id string) ([]string, error) {
	users := map[string]bool{}
	r.mu.RLock()
	for key, def := range r.agents {
		if slices.Contains(def.Connectors, id) {
			users[key] = true
		}
	}
	r.mu.RUnlock()
	var sourceErr error
	err := visitRuntimeEntries(r.cfg.Paths.AgentsDir, nil, func(name string, _ os.DirEntry) bool {
		return !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
	}, func(name string, entry os.DirEntry) {
		if sourceErr != nil {
			return
		}
		source, ok := runtimeAgentSource(r.cfg.Paths.AgentsDir, name, entry)
		if !ok {
			return
		}
		data, err := os.ReadFile(source.Path)
		var root map[string]any
		var ids []string
		if err == nil {
			root, ids, err = agentConnectorTree(string(data))
		}
		if err != nil {
			sourceErr = fmt.Errorf("cannot check connector references for agent %s: %w", name, err)
			return
		}
		if slices.Contains(ids, id) {
			users[adminAgentKey(source, adminAgentFallbackKey(source), root)] = true
		}
	})
	if err != nil {
		return nil, err
	}
	if sourceErr != nil {
		return nil, sourceErr
	}
	keys := make([]string, 0, len(users))
	for key := range users {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}
