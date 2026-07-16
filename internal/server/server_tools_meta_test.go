package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/tools"
)

type adminToolsStubExecutor struct {
	defs []api.ToolDetailResponse
}

func (s adminToolsStubExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), s.defs...)
}

func (s adminToolsStubExecutor) Invoke(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return contracts.ToolExecutionResult{}, nil
}

func TestAdminToolsListIgnoresQueryAndFlattensMetadata(t *testing.T) {
	server := &Server{deps: Dependencies{Tools: adminToolsStubExecutor{defs: []api.ToolDetailResponse{
		{
			Key:  "bash",
			Name: "bash",
			Meta: map[string]any{
				"kind":           "backend",
				"sourceType":     "local",
				"sourceCategory": "platform",
			},
		},
		{
			Key:  "qs_read",
			Name: "qs_read",
			Meta: map[string]any{
				"kind":           "external",
				"sourceType":     "agent-local",
				"sourceCategory": "external",
			},
		},
		{
			Key:  "remote_tool",
			Name: "remote_tool",
			Meta: map[string]any{
				"kind":           "backend",
				"sourceType":     "mcp",
				"sourceCategory": "mcp",
				"serverKey":      "demo",
			},
		},
		{
			Key:  "agent_delegate",
			Name: "agent_delegate",
			Meta: map[string]any{
				"kind":           "backend",
				"sourceType":     "local",
				"sourceCategory": "platform",
				"catalogVisible": false,
			},
		},
	}}}}

	all := requestAdminTools(t, server, "/api/admin/tools")
	if len(all) != 3 {
		t.Fatalf("expected all three tools, got %#v", all)
	}
	assertToolSummary(t, all, "bash", "backend", "local", "platform", "")
	assertToolSummary(t, all, "qs_read", "external", "agent-local", "external", "")
	assertToolSummary(t, all, "remote_tool", "backend", "mcp", "mcp", "demo")
	assertAdminToolsResponseOmitsMeta(t, server, "/api/admin/tools")

	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?source=mcp"), []string{"bash", "qs_read", "remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?sourceCategory=mcp"), []string{"bash", "qs_read", "remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external"), []string{"bash", "qs_read", "remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external&sourceCategory=mcp"), []string{"bash", "qs_read", "remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?sourceCategory=does-not-exist"), []string{"bash", "qs_read", "remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?tag=remote"), []string{"bash", "qs_read", "remote_tool"})
}

func TestAdminToolsListHidesPrivateEmbeddedBuiltins(t *testing.T) {
	defs, err := tools.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	server := &Server{deps: Dependencies{Tools: adminToolsStubExecutor{defs: defs}}}
	items := requestAdminTools(t, server, "/api/admin/tools")
	visible := make(map[string]bool, len(items))
	for _, item := range items {
		visible[item.Name] = true
	}
	for _, hiddenName := range []string{
		"agent_delegate", "_session_search_", "_skill_candidate_list_", "_skill_candidate_write_",
		"memory_timeline", "memory_update", "memory_write", "memory_read", "memory_promote", "memory_search", "memory_consolidate", "memory_forget",
	} {
		if visible[hiddenName] {
			t.Errorf("private embedded builtin tool %q appeared in /api/admin/tools", hiddenName)
		}
	}
	for _, publicName := range []string{"bash", "file_read", "kbase_search", "web_fetch"} {
		if !visible[publicName] {
			t.Errorf("public embedded builtin tool %q is missing from /api/admin/tools", publicName)
		}
	}
}

func requestAdminTools(t *testing.T, server *Server, path string) []api.ToolSummary {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	server.handleTools(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.ToolSummary]
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 {
		t.Fatalf("expected success response, got %#v", response)
	}
	return response.Data
}

func assertToolSummary(t *testing.T, tools []api.ToolSummary, name string, wantKind string, wantSourceType string, wantSourceCategory string, wantServerKey string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if tool.Kind != wantKind {
			t.Fatalf("tool %s kind = %q, want %q", name, tool.Kind, wantKind)
		}
		if tool.SourceType != wantSourceType {
			t.Fatalf("tool %s sourceType = %q, want %q", name, tool.SourceType, wantSourceType)
		}
		if tool.SourceCategory != wantSourceCategory {
			t.Fatalf("tool %s sourceCategory = %q, want %q", name, tool.SourceCategory, wantSourceCategory)
		}
		if tool.ServerKey != wantServerKey {
			t.Fatalf("tool %s serverKey = %q, want %q", name, tool.ServerKey, wantServerKey)
		}
		return
	}
	t.Fatalf("tool %s not found in %#v", name, tools)
}

func assertAdminToolsResponseOmitsMeta(t *testing.T, server *Server, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	server.handleTools(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]map[string]any]
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, tool := range response.Data {
		if _, ok := tool["meta"]; ok {
			t.Fatalf("expected /api/admin/tools item to omit meta, got %#v", tool)
		}
		if tool["sourceType"] == "mcp" {
			if tool["serverKey"] == "" {
				t.Fatalf("expected mcp tool to include serverKey, got %#v", tool)
			}
			continue
		}
		if _, ok := tool["serverKey"]; ok {
			t.Fatalf("expected non-mcp tool to omit serverKey, got %#v", tool)
		}
	}
}

func assertToolNames(t *testing.T, tools []api.ToolSummary, want []string) {
	t.Helper()
	if len(tools) != len(want) {
		t.Fatalf("tool names length = %d, want %d: %#v", len(tools), len(want), tools)
	}
	for i, tool := range tools {
		if tool.Name != want[i] {
			t.Fatalf("tool names[%d] = %q, want %q: %#v", i, tool.Name, want[i], tools)
		}
	}
}
