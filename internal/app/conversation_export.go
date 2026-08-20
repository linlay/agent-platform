package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const conversationTemplateFilename = "conversation.template.html"

func loadConversationHTMLTemplate() ([]byte, error) {
	candidates := make([]string, 0, 2)
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(
			filepath.Dir(executable),
			"..",
			"resources",
			"export",
			conversationTemplateFilename,
		))
	}
	candidates = append(candidates, filepath.Join(
		"scripts",
		"release-assets",
		"conversation-export",
		conversationTemplateFilename,
	))
	for _, candidate := range candidates {
		content, err := os.ReadFile(filepath.Clean(candidate))
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read conversation export template: %w", err)
		}
	}
	return nil, os.ErrNotExist
}
