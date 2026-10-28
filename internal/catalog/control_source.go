package catalog

// RuntimePublicationPending observes the existing lease-driven publication
// state without changing the lifetime of any execution snapshot.
func (r *FileRegistry) RuntimePublicationPending(key string) bool {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	return r.runtimePending[key]
}
