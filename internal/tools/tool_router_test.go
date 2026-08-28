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

type stubMCPServerToolCatalog struct {
	defs []api.ToolDetailResponse
}

func (s stubMCPServerToolCatalog) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), s.defs...)
}

func (s stubMCPServerToolCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, def := range s.defs {
		if strings.EqualFold(strings.TrimSpace(def.Name), strings.TrimSpace(name)) || strings.EqualFold(strings.TrimSpace(def.Key), strings.TrimSpace(name)) {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (s stubMCPServerToolCatalog) ToolNamesForServers(serverKeys []string) []string {
	selected := map[string]struct{}{}
	for _, serverKey := range serverKeys {
		selected[strings.ToLower(strings.TrimSpace(serverKey))] = struct{}{}
	}
	result := []string{}
	for _, def := range s.defs {
		serverKey, _ := def.Meta["serverKey"].(string)
		if _, ok := selected[strings.ToLower(strings.TrimSpace(serverKey))]; ok {
			result = append(result, def.Name)
		}
	}
	return result
}

func mustNewToolRouter(t *testing.T, backend ToolExecutor, mcp McpClient, mcpTools toolCatalog, interaction interactionSubmitter, extraDefs ...api.ToolDetailResponse) *ToolRouter {
	t.Helper()
	router, err := NewToolRouter(backend, mcp, mcpTools, interaction, extraDefs...)
	if err != nil {
		t.Fatalf("new tool router: %v", err)
	}
	return router
}

func (s stubBackendToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), s.defs...)
}

func TestToolRouterResolvesSelectedMCPServersWithoutLocalNameCollision(t *testing.T) {
	backend := stubBackendToolExecutor{defs: []api.ToolDetailResponse{{Name: "shared"}}}
	mcpTools := stubMCPServerToolCatalog{defs: []api.ToolDetailResponse{
		{Name: "remote_lookup", Meta: map[string]any{"sourceType": "mcp", "serverKey": "flowCenter"}},
		{Name: "shared", Meta: map[string]any{"sourceType": "mcp", "serverKey": "flowCenter"}},
		{Name: "other_lookup", Meta: map[string]any{"sourceType": "mcp", "serverKey": "other"}},
	}}
	router := mustNewToolRouter(t, backend, nil, mcpTools, nil)
	got := router.MCPToolNamesForServers([]string{"FLOWCENTER"})
	if len(got) != 1 || got[0] != "remote_lookup" {
		t.Fatalf("selected MCP tools = %#v", got)
	}
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
		{Name: "file_read", Meta: map[string]any{"sourceCategory": "platform"}},
		{Name: "file_write", Meta: map[string]any{"sourceCategory": "platform"}},
	}}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
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

func TestPlatformControlReadOnlyPolicyUsesOperationDescriptor(t *testing.T) {
	def := api.ToolDetailResponse{Name: "platform_control", Meta: map[string]any{"readOnly": false, "operationAware": true}}
	if !allowsReadOnlyInvocation(def, true, "platform_control", map[string]any{"operation": "runtime.status"}) {
		t.Fatal("read-only platform_control operation was denied by the final router")
	}
	if allowsReadOnlyInvocation(def, true, "platform_control", map[string]any{"operation": "run.env.set"}) {
		t.Fatal("mutating platform_control operation was allowed by the final router")
	}
	if allowsReadOnlyInvocation(def, true, "platform_control", map[string]any{"operation": "future.operation"}) {
		t.Fatal("unknown platform_control operation was allowed by the final router")
	}
}

func TestToolRouterRejectsUnregisteredToolWithoutCallingBackend(t *testing.T) {
	backend := &recordingPolicyBackend{}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
	result, err := router.Invoke(context.Background(), "missing_tool", nil, &ExecutionContext{})
	if err != nil || result.Error != "tool_not_registered" || result.ExitCode != -1 {
		t.Fatalf("unexpected unregistered result=%#v err=%v", result, err)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("unregistered tool reached backend: %#v", backend.calls)
	}
}

func (s stubBackendToolExecutor) Invoke(context.Context, string, map[string]any, *ExecutionContext) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

type captureInteractionSubmitter struct {
	hadDeadline bool
}

func (s *captureInteractionSubmitter) Handles(toolName string) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "ask_user_question")
}

func (s *captureInteractionSubmitter) Await(ctx context.Context, _ *ExecutionContext, _ map[string]any) (ToolExecutionResult, error) {
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
		Meta: map[string]any{"sourceCategory": "platform"},
	}}}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
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
		{Name: "one"},
		{Name: "two"},
	}}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
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

func TestToolRouterReloadRuntimeToolDefinitionsRejectsUnknownTool(t *testing.T) {
	root := t.TempDir()
	router := mustNewToolRouter(t, stubBackendToolExecutor{
		defs: []api.ToolDetailResponse{{Name: "datetime"}},
	}, nil, nil, nil)

	if _, ok := router.Tool("leave_form"); ok {
		t.Fatal("did not expect runtime tool before reload")
	}
	if err := os.WriteFile(filepath.Join(root, "leave_form.yml"), []byte(`
name: leave_form
description: Collect leave details.
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

	if err := router.ReloadRuntimeToolDefinitions(root); err == nil || !strings.Contains(err.Error(), "has no registered implementation") {
		t.Fatalf("expected unknown runtime tool rejection, got %v", err)
	}
}

func TestLoadRuntimeToolDefinitionsRejectsRemovedClassificationFields(t *testing.T) {
	for _, field := range []string{"type", "kind", "toolAction", "submitResultFormat"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			content := "name: datetime\n" + field + ": legacy\n"
			if err := os.WriteFile(filepath.Join(root, "datetime.yml"), []byte(content), 0o644); err != nil {
				t.Fatalf("write runtime tool: %v", err)
			}
			if _, err := LoadRuntimeToolDefinitions(root); err == nil || !strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("expected removed field %q to fail, got %v", field, err)
			}
		})
	}
}

func TestToolRouterViewportMetadataDoesNotChangeBackendRouting(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{{
		Name: "ordinary_tool",
		Meta: map[string]any{
			"viewportType": "html",
			"viewportKey":  "ordinary_card",
		},
	}}}
	interaction := &captureInteractionSubmitter{}
	router := mustNewToolRouter(t, backend, nil, nil, interaction)

	result, err := router.Invoke(context.Background(), "ordinary_tool", nil, &ExecutionContext{})
	if err != nil || result.Output != "ok" {
		t.Fatalf("invoke ordinary tool: result=%#v err=%v", result, err)
	}
	if interaction.hadDeadline || len(backend.calls) != 1 || backend.calls[0] != "ordinary_tool" {
		t.Fatalf("viewport metadata changed routing: backend=%#v interaction=%#v", backend.calls, interaction)
	}
}

func TestToolRouterMCPViewportMetadataDoesNotCreateInteractionAwaiting(t *testing.T) {
	interaction := &captureInteractionSubmitter{}
	def := api.ToolDetailResponse{
		Name: "ask_user_question",
		Meta: map[string]any{
			"sourceType":   "mcp",
			"serverKey":    "remote",
			"viewportType": "builtin",
			"viewportKey":  "question",
		},
	}
	router := mustNewToolRouter(
		t,
		stubBackendToolExecutor{},
		outputSchemaMCPClient{payload: map[string]any{"structuredContent": map[string]any{"source": "mcp"}}},
		outputSchemaToolCatalog{def: def},
		interaction,
	)

	result, err := router.Invoke(context.Background(), "ask_user_question", nil, &ExecutionContext{})
	if err != nil || result.Structured["source"] != "mcp" {
		t.Fatalf("expected MCP invocation, result=%#v err=%v", result, err)
	}
	if interaction.hadDeadline {
		t.Fatal("MCP viewport metadata must not route through the interaction handler")
	}
}

func TestToolRouterRejectsInvalidAskViewportOverlay(t *testing.T) {
	backend := stubBackendToolExecutor{defs: []api.ToolDetailResponse{{
		Name: "ask_user_question",
		Meta: map[string]any{
			"viewportType": "builtin",
			"viewportKey":  "question",
		},
	}}}
	_, err := NewToolRouter(backend, nil, nil, nil, api.ToolDetailResponse{
		Name: "ask_user_question",
		Meta: map[string]any{
			"viewportType": "builtin",
			"viewportKey":  "legacy_question_dialog",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "viewportKey=question") {
		t.Fatalf("expected invalid ask viewport overlay rejection, got %v", err)
	}
}

func TestToolRouterRequiresRegisteredAskInteractionHandler(t *testing.T) {
	backend := stubBackendToolExecutor{defs: []api.ToolDetailResponse{{
		Name: "ask_user_question",
		Meta: map[string]any{
			"viewportType": "builtin",
			"viewportKey":  "question",
		},
	}}}
	_, err := NewToolRouter(backend, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a registered interaction handler") {
		t.Fatalf("expected missing ask interaction handler rejection, got %v", err)
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
				"sourceType":     "local",
				"sourceCategory": "platform",
				"sourceKey":      "datetime",
			},
		}},
		[]api.ToolDetailResponse{{
			Name:  "datetime",
			Label: "日期时间",
			Meta: map[string]any{
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

func TestToolRouterInteractionToolDoesNotUseToolTimeoutDeadline(t *testing.T) {
	interaction := &captureInteractionSubmitter{}
	router := mustNewToolRouter(t, stubBackendToolExecutor{defs: []api.ToolDetailResponse{{
		Name: "ask_user_question",
		Meta: map[string]any{
			"sourceType":   "local",
			"viewportType": "builtin",
			"viewportKey":  "question",
		},
	}}}, nil, nil, interaction)

	result, err := router.Invoke(context.Background(), "ask_user_question", map[string]any{"mode": "question"}, &ExecutionContext{
		Budget: Budget{
			Tool: RetryPolicy{Timeout: 1},
		},
	})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful interaction result, got %#v", result)
	}
	if interaction.hadDeadline {
		t.Fatal("interaction tools should not inherit budget.tool.timeout as a context deadline")
	}
}

func TestToolInvocationResultStatus(t *testing.T) {
	tests := []struct {
		name   string
		result ToolExecutionResult
		want   string
	}{
		{name: "success", result: ToolExecutionResult{ExitCode: 0}, want: "ok"},
		{name: "process failure", result: ToolExecutionResult{ExitCode: 1}, want: "error"},
		{name: "platform failure", result: ToolExecutionResult{Error: "tool_failed"}, want: "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolInvocationResultStatus(tc.result); got != tc.want {
				t.Fatalf("toolInvocationResultStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeCompactModelOutputPolicyIsCodeOwned(t *testing.T) {
	for _, name := range []string{
		"bash", "bash_sandbox",
		"desktop_action", "desktop_cdp",
		"file_read", "file_write", "file_edit", "file_glob", "file_grep",
		"image_generate", "vision_recognize", "web_fetch",
		"regex",
	} {
		if !runtimeToolUsesCompactModelOutput(name) {
			t.Errorf("expected %s to use compact model output", name)
		}
	}
	for _, name := range []string{"datetime", "memory_read", "plan_get_tasks", "artifact_publish"} {
		if runtimeToolUsesCompactModelOutput(name) {
			t.Errorf("expected %s to keep its standard model output", name)
		}
	}
}
