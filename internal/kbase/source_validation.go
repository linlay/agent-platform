package kbase

import (
	"fmt"
	"strings"

	"agent-platform/internal/pathutil"
)

// ValidateSourceChatsSeparation rejects a knowledge source that is equal to,
// contains, or is contained by the runtime chats root. Both paths are
// canonicalized so symlink overlap is treated as real overlap.
func ValidateSourceChatsSeparation(sourceRoot string, chatsRoot string) error {
	sourceRoot = strings.TrimSpace(sourceRoot)
	chatsRoot = strings.TrimSpace(chatsRoot)
	if sourceRoot == "" || chatsRoot == "" {
		return nil
	}
	source, err := pathutil.Canonicalize(sourceRoot)
	if err != nil {
		return fmt.Errorf("resolve KBASE source root: %w", err)
	}
	chats, err := pathutil.Canonicalize(chatsRoot)
	if err != nil {
		return fmt.Errorf("resolve runtime chats root: %w", err)
	}
	if pathutil.WithinRoot(source, chats) || pathutil.WithinRoot(chats, source) {
		return fmt.Errorf("KBASE source root must be separate from the runtime chats root")
	}
	return nil
}
