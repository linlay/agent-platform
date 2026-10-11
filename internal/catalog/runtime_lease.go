package catalog

import (
	"fmt"
	"log"
	"sync"

	"agent-platform/internal/connector"
)

// RuntimeLeaser freezes Agent files together with their catalog definition.
// The caller releases the lease after a Run, child invocation, or Terminal ends.
type RuntimeLeaser interface {
	AcquireAgentRuntime(string) (AgentDefinition, func(), bool)
	AcquireTeamRuntime(string) (TeamSnapshot, func(), bool)
}

func (r *FileRegistry) AcquireAgentRuntime(key string) (AgentDefinition, func(), bool) {
	r.executionMu.Lock()
	def, ok := r.AgentDefinition(key)
	if !ok {
		r.executionMu.Unlock()
		return AgentDefinition{}, nil, false
	}
	release, checks := r.retainForVerificationLocked([]AgentDefinition{def})
	r.executionMu.Unlock()
	if !r.verifyRuntimeChecks(checks, release) {
		return AgentDefinition{}, nil, false
	}
	return def, release, true
}

func (r *FileRegistry) AcquireTeamRuntime(key string) (TeamSnapshot, func(), bool) {
	r.executionMu.Lock()
	team, ok := r.ResolveTeam(key)
	if !ok {
		r.executionMu.Unlock()
		return TeamSnapshot{}, nil, false
	}
	defs := []AgentDefinition{team.Coordinator}
	for _, key := range team.ValidAgentKeys {
		def, _ := team.AgentDefinition(key)
		defs = append(defs, def)
	}
	release, checks := r.retainForVerificationLocked(defs)
	r.executionMu.Unlock()
	if !r.verifyRuntimeChecks(checks, release) {
		return TeamSnapshot{}, nil, false
	}
	return team, release, true
}

// Capture trusted digests and retain all resources under the publication lock.
// Hashing then runs without executionMu; the provisional lease prevents GC and
// repair from removing or replacing these paths. Admission linearizes here.
type runtimeVersionCheck struct {
	def      AgentDefinition
	expected string
}

func (r *FileRegistry) retainForVerificationLocked(defs []AgentDefinition) (func(), []runtimeVersionCheck) {
	var checks []runtimeVersionCheck
	for _, def := range defs {
		if r.assembler != nil && def.RuntimeRevision != "" {
			checks = append(checks, runtimeVersionCheck{def, r.assembler.contentDigests[def.RuntimeDir]})
		}
	}
	return r.retainDefinitionsLocked(defs), checks
}

func (r *FileRegistry) verifyRuntimeChecks(checks []runtimeVersionCheck, release func()) bool {
	for _, check := range checks {
		var err error
		if check.expected == "" {
			err = fmt.Errorf("unknown runtime version")
		} else {
			err = verifyRuntimeVersionContent(check.def.RuntimeDir, check.expected)
		}
		if err != nil {
			r.executionMu.Lock()
			alreadyPending := r.runtimePending[check.def.Key]
			r.markRuntimeUnavailable(check.def, err)
			r.executionMu.Unlock()
			release()
			// A last-user release already requests reload. Otherwise request it
			// once now while the old active users continue retaining their paths.
			r.executionMu.Lock()
			callback := r.onRuntimeIdle
			request := !alreadyPending && r.runtimePending[check.def.Key]
			r.executionMu.Unlock()
			if request && callback != nil {
				go callback()
			}
			return false
		}
	}
	return true
}

func (r *FileRegistry) retainDefinitionsLocked(defs []AgentDefinition) func() {
	keys := make([]string, 0, len(defs))
	for _, def := range defs {
		keys = append(keys, def.Key)
	}
	if r.runtimeUsers == nil {
		r.runtimeUsers = map[string]int{}
	}
	if r.runtimeVersions == nil {
		r.runtimeVersions = map[string]int{}
	}
	var versions []string
	for _, def := range defs {
		key := def.Key
		r.runtimeUsers[key]++
		dir := def.RuntimeDir
		r.runtimeVersions[dir]++
		versions = append(versions, dir)
	}
	r.mu.Lock()
	if r.liveConnectorMounts == nil {
		r.liveConnectorMounts = map[string]connector.AgentRuntime{}
		r.liveConnectorUsers = map[string]int{}
	}
	var mountKeys []string
	for _, def := range defs {
		key := def.Key
		for _, mount := range def.ConnectorMounts {
			identity := key + "\x00" + mount.Dir
			r.liveConnectorMounts[identity] = connector.AgentRuntime{AgentKey: key, ID: mount.ID, Dir: mount.Dir, Digest: mount.Digest}
			r.liveConnectorUsers[identity]++
			mountKeys = append(mountKeys, identity)
		}
	}
	r.mu.Unlock()
	r.updateRuntimeDiagnostics()
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
			r.mu.Lock()
			for _, identity := range mountKeys {
				r.liveConnectorUsers[identity]--
				if r.liveConnectorUsers[identity] == 0 {
					delete(r.liveConnectorUsers, identity)
					delete(r.liveConnectorMounts, identity)
				}
			}
			r.mu.Unlock()
			versionBecameIdle := false
			for _, dir := range versions {
				r.runtimeVersions[dir]--
				if r.runtimeVersions[dir] == 0 {
					delete(r.runtimeVersions, dir)
					versionBecameIdle = true
				}
			}
			r.updateRuntimeDiagnostics()
			// Remaining leases keep the same GC roots; avoid filesystem scans
			// until a version loses its last lease.
			if versionBecameIdle {
				r.collectRuntimeVersions()
			}
			callback := r.onRuntimeIdle
			r.executionMu.Unlock()
			if refresh && callback != nil {
				callback()
			}
		})
	}
}

// SetRuntimeReload reconciles failed source updates and old connector bindings.
func (r *FileRegistry) SetRuntimeReload(callback func()) {
	r.executionMu.Lock()
	r.onRuntimeIdle = callback
	r.executionMu.Unlock()
}

func (r *FileRegistry) snapshotPublishedRuntimes() {
	if r.assembler == nil {
		return
	}
	r.assembler.previousAgents = map[string]AgentDefinition{}
	r.assembler.previousAdmin = map[string]AdminAgent{}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, def := range r.agents {
		r.assembler.previousAgents[key] = def
		r.assembler.previousAdmin[key] = r.adminAgents[key]
	}
}

// reconcileRuntimePending runs under executionMu after an Agent reload cascade.
// Successful Agent updates publish immediately; pending tracks failures and
// connector cleanup only.
func (r *FileRegistry) reconcileRuntimePending(succeeded bool) {
	if r.assembler == nil {
		return
	}
	if r.runtimePending == nil {
		r.runtimePending = map[string]bool{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key := range r.assembler.previousAgents {
		if !succeeded || !r.assembler.refreshedAgents[key] {
			// Includes deferred content, missing/deleted sources and validation
			// failures. A failed cascade must not clear earlier pending work.
			r.runtimePending[key] = true
		} else {
			delete(r.runtimePending, key)
		}
	}
	// Connector-only updates may publish immediately while old Runs retain
	// their original versions. They still need route/pin reconciliation and
	// collection after release, even when ordinary Agent files are unchanged.
	for _, live := range r.liveConnectorMounts {
		current := false
		for _, mount := range r.agents[live.AgentKey].ConnectorMounts {
			if mount.ID == live.ID && mount.Dir == live.Dir && mount.Digest == live.Digest {
				current = true
				break
			}
		}
		if !current {
			r.runtimePending[live.AgentKey] = true
		}
	}
}

// Called under executionMu after an out-of-lock integrity check failed.
func (r *FileRegistry) markRuntimeUnavailable(def AgentDefinition, err error) {
	log.Printf("[catalog][runtime] agent=%s revision=%s unavailable: %v", def.Key, def.RuntimeRevision, err)
	if r.runtimePending == nil {
		r.runtimePending = map[string]bool{}
	}
	if !r.runtimePending[def.Key] {
		r.runtimePending[def.Key] = true
	}
}

// AcquireAgentSnapshot extends an existing process-local lease, never reloads a source.
func (r *FileRegistry) AcquireAgentSnapshot(def AgentDefinition) (AgentDefinition, func(), bool) {
	r.executionMu.Lock()
	if r.runtimeVersions[def.RuntimeDir] == 0 {
		r.executionMu.Unlock()
		return AgentDefinition{}, nil, false
	}
	def = cloneAgentDefinitionSnapshot(def)
	release, checks := r.retainForVerificationLocked([]AgentDefinition{def})
	r.executionMu.Unlock()
	if !r.verifyRuntimeChecks(checks, release) {
		return AgentDefinition{}, nil, false
	}
	return def, release, true
}
func (r *FileRegistry) AcquireTeamSnapshot(team TeamSnapshot) (TeamSnapshot, func(), bool) {
	r.executionMu.Lock()
	if r.runtimeVersions[team.Coordinator.RuntimeDir] == 0 {
		r.executionMu.Unlock()
		return TeamSnapshot{}, nil, false
	}
	defs := []AgentDefinition{team.Coordinator}
	for _, key := range team.ValidAgentKeys {
		def, ok := team.AgentDefinition(key)
		if !ok || r.runtimeVersions[def.RuntimeDir] == 0 {
			r.executionMu.Unlock()
			return TeamSnapshot{}, nil, false
		}
		defs = append(defs, def)
	}
	release, checks := r.retainForVerificationLocked(defs)
	r.executionMu.Unlock()
	if !r.verifyRuntimeChecks(checks, release) {
		return TeamSnapshot{}, nil, false
	}
	return team, release, true
}

// AcquireRunRuntime freezes a root Agent and, for TEAM, its complete roster
// under the same publication lock. Mode changes cannot split admission across
// two catalog revisions.
func (r *FileRegistry) AcquireRunRuntime(key string) (AgentDefinition, *TeamSnapshot, func(), bool) {
	r.executionMu.Lock()
	r.mu.RLock()
	def, ok := r.agents[key]
	if !ok {
		r.mu.RUnlock()
		r.executionMu.Unlock()
		return AgentDefinition{}, nil, nil, false
	}
	def = cloneAgentDefinitionSnapshot(def)
	var snapshot *TeamSnapshot
	defs := []AgentDefinition{def}
	if def.Mode == "TEAM" {
		if def.TeamConfig == nil {
			r.mu.RUnlock()
			r.executionMu.Unlock()
			return AgentDefinition{}, nil, nil, false
		}
		team := resolveTeamSnapshotLocked(def, r.agents)
		snapshot = &team
		for _, member := range team.ValidAgentKeys {
			d, _ := team.AgentDefinition(member)
			defs = append(defs, d)
		}
	}
	r.mu.RUnlock()
	release, checks := r.retainForVerificationLocked(defs)
	r.executionMu.Unlock()
	if !r.verifyRuntimeChecks(checks, release) {
		return AgentDefinition{}, nil, nil, false
	}
	return def, snapshot, release, true
}
