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

type captureFrontendSubmitter struct {
	hadDeadline bool
}

func (s *captureFrontendSubmitter) Await(ctx context.Context, _ *ExecutionContext, _ map[string]any) (ToolExecutionResult, error) {
	_, s.hadDeadline = ctx.Deadline()
	return ToolExecutionResult{Output: "ok", ExitCode: 0}, nil
}

type captureExternalInvoker struct {
	configured []api.ToolDetailResponse
	invoked    string
	args       map[string]any
}

func (s *captureExternalInvoker) Configure(defs []api.ToolDetailResponse) {
	s.configured = append([]api.ToolDetailResponse(nil), defs...)
}

func (s *captureExternalInvoker) Invoke(_ context.Context, def api.ToolDetailResponse, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	s.invoked = def.Name
	s.args = args
	return structuredResult(map[string]any{"tool": def.Name, "ok": true, "args": args}), nil
}

func (s *captureExternalInvoker) Close() error {
	return nil
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

func TestToolRouterReloadRuntimeExternalToolDefinitions(t *testing.T) {
	root := t.TempDir()
	external := &captureExternalInvoker{}
	router := NewToolRouter(stubBackendToolExecutor{}, nil, nil, nil, nil).WithExternalInvoker(external)
	if err := os.WriteFile(filepath.Join(root, "qs_read.yml"), []byte(`
name: qs_read
description: Read Qiuer method.
external:
  transport: stdio-jsonrpc
  serviceKey: qiuerscript
  command: ./qiuerscript-tool
  args: ["serve"]
inputSchema:
  type: object
  properties:
    file_path:
      type: string
`), 0o644); err != nil {
		t.Fatalf("write runtime tool: %v", err)
	}

	if err := router.ReloadRuntimeToolDefinitions(root); err != nil {
		t.Fatalf("reload runtime tools: %v", err)
	}
	tool, ok := router.Tool("qs_read")
	if !ok {
		t.Fatal("expected runtime external tool after reload")
	}
	if tool.Meta["kind"] != "external" || tool.Meta["serviceKey"] != "qiuerscript" {
		t.Fatalf("unexpected runtime tool metadata %#v", tool.Meta)
	}
	externalMeta, _ := tool.Meta["external"].(map[string]any)
	if externalMeta["command"] != filepath.Join(root, "qiuerscript-tool") {
		t.Fatalf("expected relative command to resolve from manifest dir, got %#v", externalMeta["command"])
	}
	if len(external.configured) == 0 {
		t.Fatal("expected external invoker to be configured after reload")
	}
}

func TestLoadRuntimeToolDefinitionsBindsBundleService(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "qiuerscript")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "service.yml"), []byte(`
key: qiuerscript
transport: stdio-jsonrpc
command: ./qiuerscript-tool
args: ["serve", "--datasource", "dev"]
startupTimeout: 5
timeout: 30
`), 0o644); err != nil {
		t.Fatalf("write service: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "qs_read.yml"), []byte(`
name: qs_read
label: Read QS
description: Read Qiuer method.
submitResultFormat: json-compact
type: function
inputSchema:
  type: object
  properties:
    file_path:
      type: string
  required:
    - file_path
`), 0o644); err != nil {
		t.Fatalf("write runtime tool: %v", err)
	}

	defs, err := LoadRuntimeToolDefinitions(root)
	if err != nil {
		t.Fatalf("load runtime tools: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected only qs_read to load, got %#v", defs)
	}
	tool := defs[0]
	if tool.Name != "qs_read" {
		t.Fatalf("expected qs_read, got %q", tool.Name)
	}
	if tool.Meta["kind"] != "external" || tool.Meta["serviceKey"] != "qiuerscript" {
		t.Fatalf("unexpected runtime tool metadata %#v", tool.Meta)
	}
	if _, exists := tool.Meta["explicitOnly"]; exists {
		t.Fatalf("did not expect explicitOnly from bundle service, got %#v", tool.Meta)
	}
	externalMeta, _ := tool.Meta["external"].(map[string]any)
	if externalMeta["command"] != filepath.Join(bundle, "qiuerscript-tool") {
		t.Fatalf("expected bundle command to resolve from service dir, got %#v", externalMeta["command"])
	}
	if externalMeta["workingDirectory"] != bundle {
		t.Fatalf("expected bundle working directory, got %#v", externalMeta["workingDirectory"])
	}
}

func TestToolRouterInvokeExternalTool(t *testing.T) {
	external := &captureExternalInvoker{}
	router := NewToolRouter(stubBackendToolExecutor{}, nil, nil, nil, nil, api.ToolDetailResponse{
		Name: "qs_read",
		Meta: map[string]any{
			"kind":       "external",
			"sourceType": "agent-local",
			"external": map[string]any{
				"transport":  "stdio-jsonrpc",
				"serviceKey": "qiuerscript",
				"command":    "/bin/qs",
			},
		},
	}).WithExternalInvoker(external)

	result, err := router.Invoke(context.Background(), "qs_read", map[string]any{"file_path": "10.69"}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke external tool: %v", err)
	}
	if result.ExitCode != 0 || external.invoked != "qs_read" {
		t.Fatalf("unexpected external invocation result=%#v invoked=%q", result, external.invoked)
	}
	if external.args["file_path"] != "10.69" {
		t.Fatalf("unexpected args %#v", external.args)
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
