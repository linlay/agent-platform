package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/mcp"
	"agent-platform/internal/ws"
)

func TestAdminAgentConnectorMCPStateUsesCurrentPublishedVersion(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	old, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("cannot acquire old runtime")
	}
	t.Cleanup(release)
	oldMount := old.ConnectorMounts[slices.IndexFunc(old.ConnectorMounts, func(m catalog.ConnectorMount) bool { return m.ID == "docs" })]
	oldKey := connector.AgentVersionServerKey(old.Key, "docs", oldMount.Digest)
	statuses := map[string]api.MCPServerToolSyncStatus{oldKey: {Status: "ready"}}
	f.server.deps.MCPToolSyncStatus = stubMCPToolSyncStatusProvider{statuses: statuses}
	f.server.deps.Tools = adminToolsStubExecutor{defs: []api.ToolDetailResponse{
		{Name: "old_docs_search", Meta: map[string]any{"sourceType": "mcp", "sourceCategory": "mcp", "serverKey": oldKey}},
	}}
	manifestPath := filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), "docs", "connector.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(manifest, &value); err != nil {
		t.Fatal(err)
	}
	value["version"] = "1.0.1"
	manifest, _ = json.Marshal(value)
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(context.Background(), "connectors"); err != nil {
		t.Fatal(err)
	}
	current, ok := registry.AgentDefinition("mock-agent")
	if !ok || current.RuntimeRevision == old.RuntimeRevision {
		t.Fatal("new runtime was not published")
	}
	currentMount := current.ConnectorMounts[slices.IndexFunc(current.ConnectorMounts, func(m catalog.ConnectorMount) bool { return m.ID == "docs" })]
	if currentMount.Digest == oldMount.Digest {
		t.Fatal("connector digest did not change")
	}
	if _, err := os.Stat(oldMount.Dir); err != nil {
		t.Fatalf("old leased package not retained: %v", err)
	}
	read := func() api.AdminAgentConnectorsResponse {
		return agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	}
	assertDocs := func(response api.AdminAgentConnectorsResponse, status string, count int) {
		t.Helper()
		if response.ReloadPending || response.RuntimeRevision != current.RuntimeRevision {
			t.Fatalf("not the published snapshot: %+v", response)
		}
		docs := []api.AgentConnectorMCPStatus{}
		for _, component := range response.MCP {
			if component.ConnectorID == "docs" {
				docs = append(docs, component)
			}
		}
		if len(docs) != 1 || docs[0].ServerKey != "docs" || docs[0].Status != status || docs[0].ToolCount != count {
			t.Fatalf("old or foreign instance satisfied current snapshot: %+v", docs)
		}
	}
	// An old Run's ready instance and an unrelated Agent's ready instance cannot
	// make the newly published package appear ready.
	currentKey := connector.AgentVersionServerKey(current.Key, "docs", currentMount.Digest)
	foreignKey := connector.AgentVersionServerKey("another-agent", "docs", currentMount.Digest)
	statuses[foreignKey] = api.MCPServerToolSyncStatus{Status: "ready"}
	assertDocs(read(), "pending", 0)
	statuses[currentKey] = api.MCPServerToolSyncStatus{Status: "syncing"}
	assertDocs(read(), "syncing", 0)
	statuses[currentKey] = api.MCPServerToolSyncStatus{Status: "ready"}
	assertDocs(read(), "ready", 0)
	f.server.deps.Tools = adminToolsStubExecutor{defs: []api.ToolDetailResponse{
		{Name: "old_docs_search", Meta: map[string]any{"sourceType": "mcp", "sourceCategory": "mcp", "serverKey": oldKey}},
		{Name: "foreign_docs_search", Meta: map[string]any{"sourceType": "mcp", "sourceCategory": "mcp", "serverKey": foreignKey}},
		{Name: "current_docs_search", Meta: map[string]any{"sourceType": "mcp", "sourceCategory": "mcp", "serverKey": currentKey}},
	}}
	assertDocs(read(), "ready", 1)
	statuses[currentKey] = api.MCPServerToolSyncStatus{Status: "unavailable"}
	assertDocs(read(), "unavailable", 0)
	statuses[currentKey] = api.MCPServerToolSyncStatus{Status: "unknown-future-status"}
	assertDocs(read(), "unavailable", 0)
}

func TestAdminAgentConnectorMCPStateIncludesPresetsAndIsReadOnly(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), "docs")
		cfg.PresetConnectors = []string{"docs"}
	}})
	source, err := f.registry.(*catalog.FileRegistry).ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	response := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if len(response.MCP) != 1 || response.MCP[0].ConnectorID != "docs" || response.MCP[0].Status != "pending" || response.RuntimeRevision == "" {
		t.Fatalf("preset or pending state missing: %+v", response)
	}
	after, err := f.registry.(*catalog.FileRegistry).ReadEditableAgentSource("mock-agent")
	if err != nil || after.SHA256 != source.SHA256 {
		t.Fatalf("reading discovery state mutated Agent source: %v", err)
	}
}

func TestAdminAgentConnectorMCPStateReturnsAnEmptyArrayWithoutMCP(t *testing.T) {
	f := newTestFixture(t)
	rec := agentConnectorRequest(f.server, "GET", "mock-agent", nil)
	response := agentConnectorResponse(t, rec)
	if response.MCP == nil || len(response.MCP) != 0 {
		t.Fatalf("empty MCP snapshot must be explicit: %s", rec.Body.String())
	}
}

func TestAgentConnectorMCPStateReadAndMountConsumeOnlyCachedDiscovery(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		http.Error(w, "unexpected readiness probe", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: ws.NewHub(), setupRuntime: func(_ string, cfg *config.Config) {
		writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), "docs")
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(source, []byte("\nconnectorConfig:\n  connectors:\n    - docs\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			"main": map[string]any{"type": "streamableHttp", "url": upstream.URL},
		}})
		if err := os.WriteFile(filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "docs", "mcp.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}})
	def, found := f.registry.AgentDefinition("mock-agent")
	index := slices.IndexFunc(def.ConnectorMounts, func(m catalog.ConnectorMount) bool { return m.ID == "docs" })
	if !found || index < 0 {
		t.Fatal("fixture has no current MCP mount")
	}
	mount := def.ConnectorMounts[index]
	key := connector.AgentVersionServerKey(def.Key, "docs", mount.Digest)
	f.server.deps.MCPToolSyncStatus = stubMCPToolSyncStatusProvider{statuses: map[string]api.MCPServerToolSyncStatus{key: {Status: "ready"}}}
	f.server.deps.Tools = adminToolsStubExecutor{defs: []api.ToolDetailResponse{
		{Name: "docs_search", Meta: map[string]any{"sourceType": "mcp", "sourceCategory": "mcp", "serverKey": key}},
	}}
	managed := agentConnectorResponse(t, agentConnectorRequest(f.server, http.MethodGet, "mock-agent", nil))
	if len(managed.MCP) != 1 || managed.MCP[0].Status != "ready" || managed.MCP[0].ToolCount != 1 {
		t.Fatalf("cached MCP state missing: %+v", managed)
	}
	// A no-op mount still needs the richer snapshot immediately, because the
	// Desktop "go to chat" flow waits on this PUT response before polling GET.
	updated := agentConnectorResponse(t, agentConnectorRequest(f.server, http.MethodPut, "mock-agent", map[string]any{"agentKey": "mock-agent", "connectorId": "docs", "enabled": true}))
	if !reflect.DeepEqual(updated.MCP, managed.MCP) || updated.RuntimeRevision != managed.RuntimeRevision {
		t.Fatalf("mount response lost its discovery snapshot: %+v", updated)
	}
	// The paired usage surfaces share the same state calculation while keeping
	// their established public projection, rather than leaking management data.
	rec := connectorUsageRequest(f.server, http.MethodGet, "/api/agents/connectors?agentKey=mock-agent", nil)
	var expected api.ApiResponse[api.AgentConnectorsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &expected); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("HTTP usage snapshot failed: %s %v", rec.Body.String(), err)
	}
	conn := connectorUsageSocket(t, f.server)
	sendConnectorUsageRequest(t, conn, "/api/agents/connectors", "cached-read", map[string]any{"agentKey": "mock-agent"})
	public := waitForWebSocketResponseData[api.AgentConnectorsResponse](t, conn, "cached-read")
	if !reflect.DeepEqual(public, expected.Data) {
		t.Fatalf("HTTP and WS published projections differ: %+v %+v", public, expected.Data)
	}
	if probes.Load() != 0 {
		t.Fatalf("reading readiness contacted upstream %d times", probes.Load())
	}
}

func TestAdminAgentConnectorMCPStateSkipsDisabledComponents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config map[string]any
		count  int
	}{
		{"disabled", map[string]any{"disabled": true}, 0},
		{"platform disabled", map[string]any{"platform": map[string]any{"enabled": false}}, 0},
		{"platform off", map[string]any{"platform": map[string]any{"enabled": "off"}}, 0},
		{"disabled overrides platform enabled", map[string]any{"disabled": true, "platform": map[string]any{"enabled": true}}, 0},
		{"not disabled overrides platform disabled", map[string]any{"disabled": false, "platform": map[string]any{"enabled": false}}, 1},
		{"default enabled", map[string]any{}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
				writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), "docs")
				cfg.PresetConnectors = []string{"docs"}
				component := map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"}
				for key, value := range tc.config {
					component[key] = value
				}
				body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
					"main": component,
				}})
				if err := os.WriteFile(filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "docs", "mcp.json"), body, 0o644); err != nil {
					t.Fatal(err)
				}
				pkg, err := connector.Load(cfg.Paths.EffectiveConnectorsCenterDir(), "docs")
				if err != nil {
					t.Fatal(err)
				}
				if err := mcp.ValidateConnectorPackages([]connector.Package{pkg}); err != nil {
					t.Fatalf("fixture must use a valid MCP component: %v", err)
				}
			}})
			response := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
			if response.MCP == nil || len(response.MCP) != tc.count {
				t.Fatalf("component enablement differs from registry: %+v", response)
			}
		})
	}
}
