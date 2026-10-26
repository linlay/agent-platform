package catalog

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"agent-platform/internal/connector"
)

// ConnectorUsers includes current source references (even for invalid or not yet
// reloaded Agents) and published runtimes retained by Run/Terminal leases.
func (r *FileRegistry) ConnectorUsers(id string) ([]string, error) {
	for _, preset := range r.cfg.AllPresetConnectors() {
		if strings.EqualFold(preset, id) {
			return nil, fmt.Errorf("connector is referenced by preset-connectors in configs/agent-settings.yml; remove that configuration and restart before deleting")
		}
	}
	users := map[string]bool{}
	r.mu.RLock()
	for key, def := range r.agents {
		if slices.Contains(def.Connectors, id) {
			users[key] = true
		}
	}
	r.mu.RUnlock()
	for _, mount := range r.ConnectorRuntimes() {
		if mount.ID == id {
			users[mount.AgentKey] = true
		}
	}
	configured, err := r.ConfiguredConnectorUsers(id)
	if err != nil {
		return nil, err
	}
	for _, key := range configured {
		users[key] = true
	}
	keys := make([]string, 0, len(users))
	for key := range users {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

// ConfiguredConnectorUsers scans current source, separately from published or leased versions.
func (r *FileRegistry) ConfiguredConnectorUsers(id string) ([]string, error) {
	for _, preset := range r.cfg.AllPresetConnectors() {
		if strings.EqualFold(preset, id) {
			return nil, ErrPresetConnectorReadOnly
		}
	}
	mounts, err := r.configuredConnectorMounts()
	if err != nil {
		return nil, err
	}
	return mounts[id], nil
}

// ConfiguredConnectorMountCounts scans source once for the management directory.
func (r *FileRegistry) ConfiguredConnectorMountCounts() (map[string]int, error) {
	mounts, err := r.configuredConnectorMounts()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for id, keys := range mounts {
		counts[id] = len(keys)
	}
	return counts, nil
}
func (r *FileRegistry) configuredConnectorMounts() (map[string][]string, error) {
	mounts := map[string]map[string]bool{}
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
		for _, id := range ids {
			if mounts[id] == nil {
				mounts[id] = map[string]bool{}
			}
			mounts[id][adminAgentKey(source, adminAgentFallbackKey(source), root)] = true
		}
	})
	if err != nil {
		return nil, err
	}
	if sourceErr != nil {
		return nil, sourceErr
	}

	result := map[string][]string{}
	for id, users := range mounts {
		keys := make([]string, 0, len(users))
		for key := range users {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		result[id] = keys
	}
	return result, nil
}

// ActiveConnectorUsers reports execution leases and retained suspended Run pins.
func (r *FileRegistry) ActiveConnectorUsers(id string) ([]string, error) {
	r.mu.RLock()
	users := r.activeConnectorUsersLocked(id)
	r.mu.RUnlock()
	pins, err := r.cfg.Paths.ConnectorSources().PinnedRuntimes()
	if err != nil {
		return nil, err
	}
	for _, pin := range pins {
		if pin.ID == id && !slices.Contains(users, pin.AgentKey) {
			users = append(users, pin.AgentKey)
		}
	}
	slices.Sort(users)
	return users, nil
}
func (r *FileRegistry) activeConnectorUsersLocked(id string) []string {
	users := map[string]bool{}
	for identity, mount := range r.liveConnectorMounts {
		if mount.ID == id && r.liveConnectorUsers[identity] > 0 {
			users[mount.AgentKey] = true
		}
	}
	keys := make([]string, 0, len(users))
	for key := range users {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// BeginConnectorDeletion linearizes the live-lease check with all execution admission.
// The gate survives catalog reloads and is released after commit or rollback.
func (r *FileRegistry) BeginConnectorDeletion(id string) (func(), []string, error) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	if r.deletingConnectors[id] {
		return nil, nil, connector.ErrBusy
	}
	users, err := r.ActiveConnectorUsers(id)
	if err != nil {
		return nil, nil, err
	}
	if len(users) > 0 {
		return nil, users, nil
	}
	if r.deletingConnectors == nil {
		r.deletingConnectors = map[string]bool{}
	}
	r.deletingConnectors[id] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			r.executionMu.Lock()
			delete(r.deletingConnectors, id)
			r.executionMu.Unlock()
		})
	}, nil, nil
}
