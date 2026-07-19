package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

type recordingPolicyBackend struct {
	defs  []api.ToolDetailResponse
	calls []string
}

func (b *recordingPolicyBackend) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), b.defs...)
}

func (b *recordingPolicyBackend) Invoke(_ context.Context, name string, _ map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	b.calls = append(b.calls, name)
	return ToolExecutionResult{Output: "ok", ExitCode: 0}, nil
}

func TestToolRouterEnforcesReadOnlyExecutionPolicy(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{
		{Name: "file_read", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform"}},
		{Name: "file_write", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform"}},
	}}
	router := NewToolRouter(backend, nil, nil, nil, nil)
	execCtx := &ExecutionContext{ToolExecutionPolicy: ToolExecutionPolicyReadOnly}
	denied, err := router.Invoke(context.Background(), "file_write", map[string]any{}, execCtx)
	if err != nil {
		t.Fatalf("invoke denied tool: %v", err)
	}
	if denied.Error != "btw_tool_disabled" || len(backend.calls) != 0 {
		t.Fatalf("write tool reached backend: result=%#v calls=%#v", denied, backend.calls)
	}
	allowed, err := router.Invoke(context.Background(), "file_read", map[string]any{}, execCtx)
	if err != nil || allowed.Error != "" {
		t.Fatalf("invoke read tool: result=%#v err=%v", allowed, err)
	}
	if len(backend.calls) != 1 || backend.calls[0] != "file_read" {
		t.Fatalf("expected only read invocation, got %#v", backend.calls)
	}
}

func (s stubBackendToolExecutor) Invoke(context.Context, string, map[string]any, *ExecutionContext) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

type captureFrontendSubmitter struct {
	hadDeadline bool
}

func (s *captureFrontendSubmitter) Await(ctx context.Context, _ *ExecutionContext, _ map[string]any) (ToolExecutionResult, error) {
	_, s.hadDeadline = ctx.Deadline()
	return ToolExecutionResult{Output: "ok", ExitCode: 0}, nil
}

type captureNamedToolHandler struct {
	names   []string
	invoked string
	args    map[string]any
}

func (h *captureNamedToolHandler) ToolNames() []string {
	return append([]string(nil), h.names...)
}

func (h *captureNamedToolHandler) Invoke(_ context.Context, toolName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	h.invoked = toolName
	h.args = args
	return ToolExecutionResult{Output: "named", ExitCode: 0}, nil
}

func TestToolRouterRegisterHandlerRoutesNormalizedBackendName(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{{
		Name: "special_lookup",
		Key:  "special_alias",
		Meta: map[string]any{"kind": "backend", "sourceCategory": "platform"},
	}}}
	router := NewToolRouter(backend, nil, nil, nil, nil)
	handler := &captureNamedToolHandler{names: []string{"  SPECIAL_LOOKUP  "}}
	if err := router.RegisterHandler(handler); err != nil {
		t.Fatalf("register handler: %v", err)
	}

	result, err := router.Invoke(context.Background(), "special_alias", map[string]any{"query": "docs"}, &ExecutionContext{})
	if err != nil || result.Output != "named" {
		t.Fatalf("invoke named handler: result=%#v err=%v", result, err)
	}
	if handler.invoked != "special_lookup" || handler.args["query"] != "docs" {
		t.Fatalf("unexpected named invocation name=%q args=%#v", handler.invoked, handler.args)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("named tool reached fallback backend: %#v", backend.calls)
	}
}

func TestToolRouterRegisterHandlerRejectsConflictsAtomically(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{
		{Name: "one", Meta: map[string]any{"kind": "backend"}},
		{Name: "two", Meta: map[string]any{"kind": "backend"}},
	}}
	router := NewToolRouter(backend, nil, nil, nil, nil)
	first := &captureNamedToolHandler{names: []string{"one"}}
	if err := router.RegisterHandler(first); err != nil {
		t.Fatalf("register first handler: %v", err)
	}
	if err := router.RegisterHandler(&captureNamedToolHandler{names: []string{"ONE"}}); err == nil {
		t.Fatal("expected handler conflict")
	}

	partial := &captureNamedToolHandler{names: []string{"two", "missing"}}
	if err := router.RegisterHandler(partial); err == nil {
		t.Fatal("expected undefined tool registration error")
	}
	second := &captureNamedToolHandler{names: []string{"two"}}
	if err := router.RegisterHandler(second); err != nil {
		t.Fatalf("expected failed registration to be atomic: %v", err)
	}
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
	if tool.Meta["sourceCategory"] != "external" {
		t.Fatalf("expected runtime tool sourceCategory external, got %#v", tool.Meta)
	}
}

func TestEmbeddedToolDefinitionsArePlatformSource(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tools: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("expected embedded tool definitions")
	}
	for _, def := range defs {
		if def.Meta["sourceCategory"] != "platform" {
			t.Fatalf("expected embedded tool %q sourceCategory platform, got %#v", def.Name, def.Meta)
		}
	}
}

func TestBackendOverlayKeepsPlatformSourceCategory(t *testing.T) {
	merged := MergeToolDefinitions(
		[]api.ToolDetailResponse{{
			Name: "datetime",
			Meta: map[string]any{
				"kind":           "backend",
				"sourceType":     "local",
				"sourceCategory": "platform",
				"sourceKey":      "datetime",
			},
		}},
		[]api.ToolDetailResponse{{
			Name:  "datetime",
			Label: "日期时间",
			Meta: map[string]any{
				"kind":           "backend",
				"sourceType":     "agent-local",
				"sourceCategory": "external",
				"sourceKey":      "datetime-overlay",
			},
		}},
		nil,
	)
	if len(merged) != 1 {
		t.Fatalf("expected one merged tool, got %#v", merged)
	}
	if merged[0].Label != "日期时间" {
		t.Fatalf("expected overlay label to apply, got %#v", merged[0])
	}
	if merged[0].Meta["sourceCategory"] != "platform" || merged[0].Meta["sourceType"] != "local" || merged[0].Meta["sourceKey"] != "datetime" {
		t.Fatalf("expected backend overlay to keep platform source metadata, got %#v", merged[0].Meta)
	}
}

func TestLoadRuntimeToolDefinitionsRejectsDeprecatedExternalConfigs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string
		content string
	}{
		{"service file", "service.yml", "key: qiuerscript\ntransport: stdio-jsonrpc\ncommand: ./qiuerscript-tool\n"},
		{"external type", "tool.yml", "name: qs_read\ntype: external\n"},
		{"external block", "tool.yml", "name: qs_read\nexternal:\n  command: ./qiuerscript-tool\n"},
		{"empty external block", "tool.yml", "name: qs_read\nexternal: {}\n"},
		{"external service kind", "tool.yml", "kind: external-service\ncommand: ./qiuerscript-tool\n"},
		{"invalid legacy service", "service.yaml", "not: [valid\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, tc.file), []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write deprecated config: %v", err)
			}
			if _, err := LoadRuntimeToolDefinitions(root); err == nil || !strings.Contains(err.Error(), "transport: stdio") {
				t.Fatalf("expected migration error, got %v", err)
			}
		})
	}
}

func TestNormalizeMCPResultPreservesBusinessErrorCode(t *testing.T) {
	result := normalizeMCPResult("qs_edit", map[string]any{
		"isError": true,
		"content": []any{map[string]any{"type": "text", "text": `{"error":"last_digest_required","message":"digest is required"}`}},
		"structuredContent": map[string]any{
			"error":   "last_digest_required",
			"message": "digest is required",
		},
	})
	if result.ExitCode == 0 || result.Error != "last_digest_required" {
		t.Fatalf("business error code was degraded: %#v", result)
	}
	if result.Structured["error"] != "last_digest_required" || result.Structured["message"] != "digest is required" {
		t.Fatalf("structured MCP error was not preserved: %#v", result.Structured)
	}
}

func TestToolRouterFrontendToolDoesNotUseToolTimeoutDeadline(t *testing.T) {
	frontend := &captureFrontendSubmitter{}
	router := NewToolRouter(stubBackendToolExecutor{}, nil, nil, frontend, nil, api.ToolDetailResponse{
		Name: "ask_user_question",
		Meta: map[string]any{
			"kind":       "frontend",
			"sourceType": "local",
		},
	})

	result, err := router.Invoke(context.Background(), "ask_user_question", map[string]any{"mode": "question"}, &ExecutionContext{
		Budget: Budget{
			Tool: RetryPolicy{Timeout: 1},
		},
	})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful frontend result, got %#v", result)
	}
	if frontend.hadDeadline {
		t.Fatal("frontend tools should not inherit budget.tool.timeout as a context deadline")
	}
}
