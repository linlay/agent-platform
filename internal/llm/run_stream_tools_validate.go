package llm

import (
	"fmt"
	"strings"
)

func (s *llmRunStream) validateFrontendToolArgs(toolName string, args map[string]any) error {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	toolKind, _ := tool.Meta["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(toolKind), "frontend") {
		return nil
	}
	if s.engine.frontend == nil {
		return nil
	}
	handler, ok := s.engine.frontend.Handler(toolName)
	if !ok {
		return nil
	}
	return handler.ValidateArgs(args)
}

func validateBashToolArgs(toolName string, args map[string]any) error {
	if !isBashTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "command")) == "" {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "description")) == "" {
		return fmt.Errorf("description is required for bash tools")
	}
	return nil
}

func validateWriteToolArgs(toolName string, args map[string]any) error {
	if !isWriteTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "file_path")) == "" {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "description")) == "" {
		return fmt.Errorf("description is required for write tools")
	}
	return nil
}
