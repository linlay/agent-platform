package kbx

// collectionUpdate tracks only the current library maintenance operation.
// Scheduling, persistence and restart recovery belong to kbases.
type collectionUpdate struct {
	library     library
	initialized bool
	index       *indexState
}
type updatePaths struct {
	paths       []string
	incremental bool
}
