package catalog

import "sync"

// RuntimeLeaser freezes Agent files together with their catalog definition.
// The caller releases the lease after a Run, child invocation, or Terminal ends.
type RuntimeLeaser interface {
	AcquireAgentRuntime(string) (AgentDefinition, func(), bool)
	AcquireTeamRuntime(string) (TeamSnapshot, func(), bool)
}

func (r *FileRegistry) AcquireAgentRuntime(key string) (AgentDefinition, func(), bool) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	def, ok := r.AgentDefinition(key)
	if !ok {
		return AgentDefinition{}, nil, false
	}
	return def, r.retainRuntimeLocked([]string{def.Key}), true
}

func (r *FileRegistry) AcquireTeamRuntime(key string) (TeamSnapshot, func(), bool) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	team, ok := r.ResolveTeam(key)
	if !ok {
		return TeamSnapshot{}, nil, false
	}
	return team, r.retainRuntimeLocked(team.ValidAgentKeys), true
}

func (r *FileRegistry) retainRuntimeLocked(keys []string) func() {
	if r.runtimeUsers == nil {
		r.runtimeUsers = map[string]int{}
	}
	for _, key := range keys {
		r.runtimeUsers[key]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.executionMu.Lock()
			refresh := false
			for _, key := range keys {
				r.runtimeUsers[key]--
				if r.runtimeUsers[key] == 0 {
					delete(r.runtimeUsers, key)
					if r.runtimePending[key] {
						delete(r.runtimePending, key)
						refresh = true
					}
				}
			}
			callback := r.onRuntimeIdle
			r.executionMu.Unlock()
			if refresh && callback != nil {
				callback()
			}
		})
	}
}

// SetRuntimeReload connects deferred publication to the normal reload cascade.
func (r *FileRegistry) SetRuntimeReload(callback func()) {
	r.executionMu.Lock()
	r.onRuntimeIdle = callback
	r.executionMu.Unlock()
}

func (r *FileRegistry) freezeActiveRuntimes() {
	if r.assembler == nil {
		return
	}
	r.assembler.frozenAgents = map[string]AgentDefinition{}
	r.assembler.frozenAdmin = map[string]AdminAgent{}
	if r.runtimePending == nil {
		r.runtimePending = map[string]bool{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, count := range r.runtimeUsers {
		if def, ok := r.agents[key]; ok && count > 0 {
			r.assembler.frozenAgents[key] = def
			r.assembler.frozenAdmin[key] = r.adminAgents[key]
			r.runtimePending[key] = true
		}
	}
}
