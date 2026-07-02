package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
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

func TestAdminToolsSourceCategoryFiltering(t *testing.T) {
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
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?sourceCategory=mcp"), []string{"remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external"), []string{"qs_read"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external&sourceCategory=mcp"), nil)
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?sourceCategory=does-not-exist"), nil)
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
