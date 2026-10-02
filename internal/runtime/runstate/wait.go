package runstate

// SkipWait uses the exact invocation identity, including after completion.
func (m *Manager) SkipWait(runID, toolID string) string {
	control, ok := m.lookupControl(runID)
	if !ok {
		return "not_found"
	}
	return control.SkipNativeWait(toolID)
}
