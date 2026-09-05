package tools

import "agent-platform/internal/memory"

func newTestMemoryStore(root string) (*memory.SQLiteStore, error) {
	return memory.NewSQLiteStoreAtStartup(root, "memory.db")
}
