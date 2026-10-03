package catalog

// RenderDefinition serializes a validated definition with the same scalar and
// ordering rules used by the management editor.
func RenderDefinition(value map[string]any) []byte { return renderYAMLMap(value) }

// RuntimePublicationPending observes the existing lease-driven publication
// state without changing the lifetime of any execution snapshot.
func (r *FileRegistry) RuntimePublicationPending(key string) bool {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	return r.runtimePending[key]
}
