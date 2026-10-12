package runstate

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/controlscope"
)

// BindRunControlScope records the identity allowed to control a Run. Entries
// live as long as the process: a plan approved long after its planning Run was
// released still inherits that Run's owner. An existing owner is unchanged.
func (m *Manager) BindRunControlScope(runID string, scope controlscope.Scope) controlscope.Scope {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bound, ok := m.controlScopes[runID]; ok {
		return bound
	}
	if m.controlScopes == nil {
		m.controlScopes = map[string]controlscope.Scope{}
	}
	m.controlScopes[runID] = scope
	return scope
}

func (m *Manager) RunControlScope(runID string) (controlscope.Scope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope, ok := m.controlScopes[runID]
	return scope, ok
}

// RecordedRunOrigin returns the chat_start caller of a Run after the Run
// itself was released, so its creator can still read the outcome.
func (m *Manager) RecordedRunOrigin(runID string) *contracts.RunOrigin {
	m.mu.Lock()
	defer m.mu.Unlock()
	if origin, ok := m.runOrigins[runID]; ok {
		return &origin
	}
	return nil
}
