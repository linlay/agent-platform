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
	assertToolSourceCategory(t, all, "bash", "platform")
	assertToolSourceCategory(t, all, "qs_read", "external")
	assertToolSourceCategory(t, all, "remote_tool", "mcp")

	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?source=platform"), []string{"bash"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?source=external"), []string{"qs_read"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?source=mcp"), []string{"remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?sourceCategory=mcp"), []string{"remote_tool"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external"), []string{"qs_read"})
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?kind=external&source=mcp"), nil)
	assertToolNames(t, requestAdminTools(t, server, "/api/admin/tools?source=does-not-exist"), nil)
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

func assertToolSourceCategory(t *testing.T, tools []api.ToolSummary, name string, want string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if tool.SourceCategory != want {
			t.Fatalf("tool %s sourceCategory = %q, want %q", name, tool.SourceCategory, want)
		}
		if tool.Meta["sourceCategory"] != want {
			t.Fatalf("tool %s meta.sourceCategory = %#v, want %q", name, tool.Meta["sourceCategory"], want)
		}
		return
	}
	t.Fatalf("tool %s not found in %#v", name, tools)
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
