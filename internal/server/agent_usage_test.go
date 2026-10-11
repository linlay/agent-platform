package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/connectortest"
	"agent-platform/internal/mcp"
)

func TestAgentUsageHTTPAndWSKeepAssociationsMinimalDuringPendingPublication(t *testing.T) {
	f := connectorUsageFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Error(err)
		}
	})
	conn := connectorUsageSocket(t, f.server)
	read := func(id string, want []string) {
		t.Helper()
		rec := connectorUsageRequest(f.server, http.MethodGet, "/api/agent?agentKey=mock-agent", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("agent: %d %s", rec.Code, rec.Body.String())
		}
		var body api.ApiResponse[map[string]json.RawMessage]
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"toolBindings", "reloadPending", "connectorSelection"} {
			if body.Data[key] != nil {
				t.Fatalf("non-association field leaked: %s", rec.Body.String())
			}
		}
		for _, key := range []string{"tools", "skills", "connectors"} {
			var ids []string
			if err := json.Unmarshal(body.Data[key], &ids); err != nil || ids == nil {
				t.Fatalf("%s must be IDs: %s (%v)", key, rec.Body.String(), err)
			}
			if key == "connectors" && !reflect.DeepEqual(ids, want) {
				t.Fatalf("saved associations lost: %v != %v", ids, want)
			}
		}
		sendConnectorUsageRequest(t, conn, "/api/agent", id, map[string]any{"agentKey": "mock-agent"})
		wsData := waitForWebSocketResponseData[map[string]json.RawMessage](t, conn, id)
		if !reflect.DeepEqual(wsData, body.Data) {
			t.Fatalf("HTTP/WS Agent mismatch: %+v %+v", wsData, body.Data)
		}
	}
	read("initial", []string{"meeting"})
	rec := connectorUsageRequest(f.server, http.MethodGet, "/api/admin/agents/detail?agentKey=mock-agent", nil)
	var admin api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &admin); err != nil || rec.Code != 200 || len(admin.Data.ToolBindings) == 0 {
		t.Fatalf("management bindings lost: %s (%v)", rec.Body.String(), err)
	}
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing runtime")
	}
	t.Cleanup(release)
	rec = connectorUsageRequest(f.server, http.MethodPut, "/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	read("pending", []string{})
	catalogRec := connectorUsageRequest(f.server, http.MethodGet, "/api/connectors?agentKey=mock-agent", nil)
	var catalogBody api.ApiResponse[api.ConnectorOptionsResponse]
	if err := json.Unmarshal(catalogRec.Body.Bytes(), &catalogBody); err != nil || catalogBody.Data.ReloadPending == nil || *catalogBody.Data.ReloadPending {
		t.Fatalf("new version was not published: %s (%v)", catalogRec.Body.String(), err)
	}
	release()
	read("published", []string{})
	catalogRec = connectorUsageRequest(f.server, http.MethodGet, "/api/connectors?agentKey=mock-agent", nil)
	if err := json.Unmarshal(catalogRec.Body.Bytes(), &catalogBody); err != nil || catalogBody.Data.ReloadPending == nil || *catalogBody.Data.ReloadPending {
		t.Fatalf("pending did not clear: %s (%v)", catalogRec.Body.String(), err)
	}
}

func TestConnectorUsageDirectoryIncludesLocalReadinessAndScopedMCPStatus(t *testing.T) {
	f := connectorUsageFixture(t)
	provider := f.registry.(mcp.AgentConnectorSource)
	statuses := map[string]api.MCPServerToolSyncStatus{}
	for _, mount := range provider.ConnectorRuntimes() {
		pkg, err := f.server.connectorSources().Load(mount.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, sourceKey := range pkg.ServerKeys() {
			statuses[connector.AgentVersionServerKey(mount.AgentKey, sourceKey, mount.Digest)] = api.MCPServerToolSyncStatus{Status: "unavailable"}
		}
	}
	f.server.deps.MCPToolSyncStatus = stubMCPToolSyncStatusProvider{statuses: statuses}
	rec := connectorUsageRequest(f.server, http.MethodGet, "/api/connectors?agentKey=mock-agent", nil)
	var body api.ApiResponse[api.ConnectorOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("directory: %s (%v)", rec.Body.String(), err)
	}
	if body.Data.AgentKey != "mock-agent" || body.Data.ReloadPending == nil || *body.Data.ReloadPending {
		t.Fatalf("bad context: %+v", body.Data)
	}
	var meeting, mail, native bool
	for _, item := range body.Data.Connectors {
		if item.Readiness == "" {
			t.Fatalf("missing readiness: %+v", item)
		}
		switch item.ID {
		case "meeting":
			meeting = true
			if len(item.MCP) != 1 || item.MCP[0].AgentKey != "mock-agent" || item.MCP[0].Status != "unavailable" {
				t.Fatalf("live MCP failure lost: %+v", item)
			}
		case "mail":
			mail = true
			if len(item.MCP) != 1 || item.MCP[0].AgentKey != "" || item.MCP[0].Status != "unmounted" {
				t.Fatalf("unmounted conflated with unhealthy: %+v", item)
			}
		case connector.PlatformControlConnectorID:
			native = true
			if item.Readiness != "no_auth" {
				t.Fatalf("native auth snapshot: %+v", item)
			}
		}
		raw, _ := json.Marshal(item)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		for _, forbidden := range []string{"authentication", "authorizationUrl", "configured", "capabilities", "version", "skills", "tools"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("management data leaked: %s", raw)
			}
		}
	}
	if !meeting || !mail || !native {
		t.Fatalf("missing snapshots: %+v", body.Data)
	}
	conn := connectorUsageSocket(t, f.server)
	sendConnectorUsageRequest(t, conn, "/api/locale", "locale", map[string]any{"locale": "zh-CN"})
	waitForWebSocketResponseData[map[string]any](t, conn, "locale")
	sendConnectorUsageRequest(t, conn, "/api/connectors", "directory", map[string]any{"agentKey": "mock-agent"})
	actual := waitForWebSocketResponseData[api.ConnectorOptionsResponse](t, conn, "directory")
	if !reflect.DeepEqual(actual, body.Data) {
		t.Fatalf("directory HTTP/WS mismatch: %+v %+v", actual, body.Data)
	}
	global, err := f.server.listSelectableConnectors(context.Background(), "", "zh-CN")
	if err != nil || global.ReloadPending != nil || global.AgentKey != "" {
		t.Fatalf("Agent state leaked into global catalog: %+v %v", global, err)
	}
	if slices.ContainsFunc(global.Connectors, func(item api.ConnectorOption) bool { return item.ID == connector.WebControlConnectorID }) {
		t.Fatal("preset leaked")
	}
}

func TestAdminAgentSavesRetainToolBindingsOutsideUsageDetail(t *testing.T) {
	f := connectorUsageFixture(t)
	definition := map[string]any{
		"key": "managed-copy", "name": "Managed", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"},
		"toolConfig": map[string]any{"tools": []string{"datetime"}},
	}
	for _, item := range []struct {
		path    string
		payload map[string]any
	}{
		{"/api/admin/agents/create", map[string]any{"key": "managed-copy", "definition": definition}},
		{"/api/admin/agents/update", map[string]any{"key": "managed-copy", "definition": definition}},
		{"/api/admin/agents/update-name", map[string]any{"key": "managed-copy", "name": "Renamed"}},
	} {
		rec := connectorUsageRequest(f.server, http.MethodPost, item.path, item.payload)
		var response api.ApiResponse[api.AdminAgentSaveResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || len(response.Data.ToolBindings) == 0 {
			t.Fatalf("%s lost editing bindings: %s (%v)", item.path, rec.Body.String(), err)
		}
		if !slices.ContainsFunc(response.Data.ToolBindings, func(binding api.AgentToolBinding) bool {
			return binding.Name == "datetime" && binding.Source == "agent" && binding.Removable && binding.Active
		}) {
			t.Fatalf("own tool binding changed: %+v", response.Data.ToolBindings)
		}
	}
	rec := connectorUsageRequest(f.server, http.MethodGet, "/api/agent?agentKey=managed-copy", nil)
	var usage api.ApiResponse[map[string]json.RawMessage]
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil || rec.Code != http.StatusOK || usage.Data["toolBindings"] != nil {
		t.Fatalf("management bindings leaked into usage: %s (%v)", rec.Body.String(), err)
	}
}

func TestConnectorUsageDirectoryDistinguishesIdleAndFailedCLIPreparation(t *testing.T) {
	f := connectorUsageFixture(t)
	dir := filepath.Join(f.server.connectorSources().ExternalRoot, "pending-cli")
	if err := connectortest.WriteCLI(dir, "pending-cli", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{"id":"pending-cli","name":"Pending CLI","version":"1.0.0","type":"cli","auth_mode":null}`), 0644); err != nil {
		t.Fatal(err)
	}
	read := func(want string) {
		t.Helper()
		response, err := f.server.listSelectableConnectors(context.Background(), "mock-agent", "zh-CN")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range response.Connectors {
			if item.ID == "pending-cli" {
				if item.Readiness != want {
					t.Fatalf("idle preparation must not look active: %+v", item)
				}
				return
			}
		}
		t.Fatal("missing CLI option")
	}
	read("configuration_required")
	stateDir, err := connectorauth.StateDir(f.server.connectorSources().PersistentRoot(), "pending-cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"failed", "canceled"} {
		encoded, _ := json.Marshal(connectorauth.Preparation{ConnectorID: "pending-cli", Status: status})
		if err := os.WriteFile(filepath.Join(stateDir, "preparation.json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		read("unavailable")
	}
}
