package query

import (
	"path/filepath"

	"agent-platform/internal/runtime/controlscope"
)

func (s *Service) runControlScopes() controlscope.Store {
	root := s.deps.Config.Paths.StateDir
	if root == "" {
		root = filepath.Join(s.deps.Config.Paths.ChatsDir, ".state")
	}
	return controlscope.Store{Root: filepath.Join(root, "run-controls")}
}
