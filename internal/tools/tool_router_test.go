package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type stubBackendToolExecutor struct {
	defs []api.ToolDetailResponse
}

func (s stubBackendToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), s.defs...)
}

func (s stubBackendToolExecutor) Invoke(context.Context, string, map[string]any, *ExecutionContext) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

func TestToolRouterReloadRuntimeToolDefinitions(t *testing.T) {
	root := t.TempDir()
	router := NewToolRouter(stubBackendToolExecutor{
		defs: []api.ToolDetailResponse{{Name: "datetime", Meta: map[string]any{"kind": "backend"}}},
	}, nil, nil, nil, nil)

	if _, ok := router.Tool("leave_form"); ok {
		t.Fatal("did not expect runtime tool before reload")
	}
	if err := os.WriteFile(filepath.Join(root, "leave_form.yml"), []byte(`
name: leave_form
description: Collect leave details.
type: frontend
viewportType: html
viewportKey: leave_form
inputSchema:
  type: object
  properties:
    reason:
      type: string
`), 0o644); err != nil {
		t.Fatalf("write runtime tool: %v", err)
	}

	if err := router.ReloadRuntimeToolDefinitions(root); err != nil {
		t.Fatalf("reload runtime tools: %v", err)
	}
	tool, ok := router.Tool("leave_form")
	if !ok {
		t.Fatal("expected runtime frontend tool after reload")
	}
	if tool.Meta["kind"] != "frontend" || tool.Meta["viewportKey"] != "leave_form" {
		t.Fatalf("unexpected runtime tool metadata %#v", tool.Meta)
	}
}
