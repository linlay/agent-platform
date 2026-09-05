package memory

import "agent-platform/internal/api"

func NewSQLiteStore(root string, dbFileName string) (*SQLiteStore, error) {
	return newSQLiteStore(root, dbFileName, false)
}

func buildContextBundleFromStored(request ContextRequest, items []api.StoredMemoryResponse) ContextBundle {
	return buildContextBundleWithHybrid(request, items, hybridParams{})
}
