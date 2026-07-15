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

type captureExternalInvoker struct {
	configured []api.ToolDetailResponse
	invoked    string
	args       map[string]any
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

func TestToolRouterReloadRuntimeExternalToolDefinitions(t *testing.T) {
	root := t.TempDir()
	external := &captureExternalInvoker{}
	router := NewToolRouter(stubBackendToolExecutor{}, nil, nil, nil, nil).WithExternalInvoker(external)
	if err := os.WriteFile(filepath.Join(root, "qs_read.yml"), []byte(`
name: qs_read
description: Read Qiuer method.
tags: [knowledge, read]
readOnly: true
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
	if tool.Meta["kind"] != "external" || tool.Meta["serviceKey"] != "qiuerscript" || tool.Meta["readOnly"] != true {
		t.Fatalf("unexpected runtime tool metadata %#v", tool.Meta)
	}
	if tool.Meta["sourceCategory"] != "external" {
		t.Fatalf("expected runtime external tool sourceCategory external, got %#v", tool.Meta)
	}
	tags, _ := tool.Meta["tags"].([]string)
	if strings.Join(tags, ",") != "knowledge,read" {
		t.Fatalf("unexpected public tool tags %#v", tool.Meta["tags"])
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
