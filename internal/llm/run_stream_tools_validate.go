package llm

import (
	"strings"
)

func (s *llmRunStream) validateInteractionToolArgs(toolName string, args map[string]any) error {
	if s.engine.interactions == nil {
		return nil
	}
	handler, ok := s.engine.interactions.Handler(toolName)
	if !ok {
		return nil
	}
	return handler.ValidateArgs(args)
}

func validateWriteToolArgs(toolName string, args map[string]any) error {
	if !isWriteTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "file_path")) == "" {
		return nil
	}
	return nil
}
