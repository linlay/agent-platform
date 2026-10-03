package tools

import "agent-platform/internal/memory"

func newTestMemoryStore(root string) (*memory.Store, error) {
	return memory.NewStore(root, root+"/owner", nil), nil
}
