package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/tools"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestManagementIndependentToolsExcludeConnectorOwnership(t *testing.T) {
	defs, err := tools.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	defs = append(defs, api.ToolDetailResponse{Key: "mcp_demo_read", Name: "mcp_demo_read", Meta: map[string]any{"sourceType": "mcp", "serverKey": "demo"}})
	s := &Server{deps: Dependencies{Tools: adminToolsStubExecutor{defs: defs}}}
	for _, tool := range s.listTools() {
		if _, owned := connector.NativeToolConnector(tool.Name); owned || tool.SourceCategory == "mcp" {
			t.Fatalf("connector tool leaked: %+v", tool)
		}
	}
	names := []string{"chat_start", "chat_get_status", "chat_interrupt", "chat_query", "desktop_shell", "mcp_demo_read", "bash", "file_read", "custom"}
	got := s.independentAgentToolNames(names)
	if !reflect.DeepEqual(got, []string{"bash", "file_read", "custom"}) {
		t.Fatalf("tools: %v", got)
	}
	detail, err := s.withAdminAgentPrivateSkills(api.AdminAgentDetailResponse{ToolBindings: []api.AgentToolBinding{{Name: "chat_start"}, {Name: "file_read"}}})
	if err != nil || len(detail.ToolBindings) != 1 || detail.ToolBindings[0].Name != "file_read" {
		t.Fatalf("detail: %+v, %v", detail, err)
	}
	if len(names) != 9 {
		t.Fatal("input was mutated")
	}
}

func TestConnectorCatalogIncludesHiddenChatToolDescriptions(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	f.server.deps.Config.Paths.BuiltinConnectorsDir = ""
	release, err := f.server.deps.Config.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defs, err := tools.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	f.server.deps.Tools = adminToolsStubExecutor{defs: defs}
	for _, locale := range []string{"en", "zh-CN"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/admin/connectors", nil)
		req.Header.Set("Accept-Language", locale)
		f.server.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		var body struct {
			Data struct {
				Connectors []struct {
					ID    string            `json:"id"`
					Tools []api.ToolSummary `json:"tools"`
				} `json:"connectors"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range body.Data.Connectors {
			if c.ID != connector.TaskControlConnectorID {
				continue
			}
			found = true
			if len(c.Tools) != 7 {
				t.Fatalf("task tools: %+v", c.Tools)
			}
			for _, tool := range c.Tools {
				if tool.Label == "" || tool.Label == tool.Name || tool.Description == "" {
					t.Fatalf("missing presentation: %+v", tool)
				}
			}
		}
		if !found || strings.Contains(rec.Body.String(), "toolI18n") {
			t.Fatal("missing connector or leaked translations")
		}
	}
}

func TestAdminAgentSaveResponseExcludesConnectorTools(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	rec := httptest.NewRecorder()
	f.server.writeAdminAgentSaveResponse(rec, api.AgentDetailResponse{
		Key: "mock-agent", Tools: []string{"chat_start", "file_read"},
		Definition: map[string]any{"toolConfig": map[string]any{"tools": []string{"chat_start", "file_read"}}},
	}, nil)
	var body api.ApiResponse[api.AdminAgentSaveResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("save response: %s", rec.Body.String())
	}
	var wire struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire.Data["tools"]; exists {
		t.Fatal("management save leaked tools")
	}
	for _, binding := range body.Data.ToolBindings {
		if _, owned := connector.NativeToolConnector(binding.Name); owned {
			t.Fatalf("connector binding leaked: %+v", binding)
		}
	}
	if !strings.Contains(rec.Body.String(), `"tools":["chat_start","file_read"]`) {
		t.Fatalf("raw definition changed: %s", rec.Body.String())
	}
}
