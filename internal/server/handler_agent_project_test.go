package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	dto "agent-platform/internal/contracts/agentengine"
	"os/exec"
	"runtime"
)

func TestCreateProjectRejectsInvalidWorkspaceBeforeWriting(t *testing.T) {
	workspace := t.TempDir()
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		t.Run(mode, func(t *testing.T) {
			for _, root := range []string{"", "@root", filepath.VolumeName(workspace) + string(os.PathSeparator), "relative-project", filepath.Join(workspace, "missing")} {
				t.Run(root, func(t *testing.T) {
					fixture := newTestFixture(t)
					body, _ := json.Marshal(map[string]any{
						"key": "project-candidate", "isProject": true,
						"definition": map[string]any{
							"mode": mode, "modelConfig": map[string]any{"modelKey": "mock-model"},
							"runtimeConfig": map[string]any{"workspaceRoot": root},
						},
					})
					rec := httptest.NewRecorder()
					fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
					if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_project_workspace") {
						t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
					}
					if _, err := os.Stat(filepath.Join(fixture.server.deps.Config.Paths.AgentsDir, "project-candidate")); !os.IsNotExist(err) {
						t.Fatalf("invalid project wrote an Agent directory: %v", err)
					}
				})
			}
		})
	}
}

func TestCreateProjectPublishesWorkspaceIdentityWithoutPersistingIntent(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newTestFixture(t)
			workspace := t.TempDir()
			created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
				"isProject": true,
				"definition": map[string]any{
					"mode": mode, "modelConfig": map[string]any{"modelKey": "mock-model"},
					"runtimeConfig": map[string]any{"workspaceRoot": workspace},
				},
			})
			content, err := os.ReadFile(created.Source.Path)
			if err != nil || strings.Contains(string(content), "isProject") {
				t.Fatalf("creation intent persisted in Agent source: %s, %v", content, err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?hasWorkspace=true", nil))
			var response api.ApiResponse[[]api.AgentSummary]
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("project catalog: %d %s, %v", rec.Code, rec.Body.String(), err)
			}
			for _, agent := range response.Data {
				if agent.Key == created.Key && agent.WorkspaceDir == workspace {
					return
				}
			}
			t.Fatalf("created Agent missing from Projects: %#v", response.Data)
		})
	}
}

func TestOrdinaryAgentCreateRetainsRootWorkspace(t *testing.T) {
	for _, intent := range []any{nil, false} {
		fixture := newTestFixture(t)
		request := map[string]any{"definition": map[string]any{
			"mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"},
			"runtimeConfig": map[string]any{"workspaceRoot": "@root"},
		}}
		if intent != nil {
			request["isProject"] = intent
		}
		created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", request)
		definition, ok := fixture.server.deps.Registry.AgentDefinition(created.Key)
		if !ok || !definition.Workspace.HostRoot || definition.Workspace.ProjectDir() != "" {
			t.Fatalf("ordinary Agent root identity changed: %#v", definition.Workspace)
		}
	}
}

func TestACPProjectUsesPluginIdentityAndRejectsOldFields(t *testing.T) {
	f := newTestFixture(t)
	workspace := t.TempDir()
	body, _ := json.Marshal(map[string]any{"isProject": true, "definition": map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"engineId": "fixture/agent", "workspaceRoot": workspace}}})
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unsupported_deployment") {
		t.Fatalf("unavailable plugin: %d %s", rec.Code, rec.Body.String())
	}
	for _, legacy := range []string{"acpBridgeId", "acpProxyId"} {
		body, _ = json.Marshal(map[string]any{"isProject": true, "definition": map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"engineId": "fixture/agent", "workspaceRoot": workspace, legacy: "old"}}})
		rec = httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("legacy accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestACPDefaultsDoNotExposeLegacyBridgesOrNativeMode(t *testing.T) {
	f := newTestFixture(t)
	engines, err := agentengine.New(t.TempDir(), "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engines.Close()
	f.server.deps.AgentEngines = engines
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/agents/creation-defaults", nil))
	var response api.ApiResponse[map[string]any]
	json.Unmarshal(rec.Body.Bytes(), &response)
	for _, raw := range response.Data["types"].([]any) {
		option := raw.(map[string]any)
		if option["key"] != "acp" {
			continue
		}
		if option["mode"] != "" || option["unavailableCode"] != "unsupported_deployment" || option["acpBridges"] != nil {
			t.Fatalf("old ACP defaults: %+v", option)
		}
	}
}

func writeProjectEngineFile(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}
func encodeProjectEngine(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func projectEngineFixture(t *testing.T, script string) (*agentengine.Service, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("trusted filesystem fixture requires macOS; Windows native acceptance remains T10")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	delivery := filepath.Join(root, "desktop", "data", "agent-engines")
	os.MkdirAll(delivery, 0700)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(node)
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "desktop", "bin", "node")
	writeProjectEngineFile(t, managed, string(bin), 0700)
	probe := exec.Command(node, "-p", "process.versions.node")
	probe.Env = []string{}
	version, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(root, "bootstrap.json")
	writeProjectEngineFile(t, boot, string(encodeProjectEngine(t, dto.ManagementBootstrap{SchemaVersion: 1, HostID: "host", LaunchNonce: "launch", ManagementSecret: strings.Repeat("a", 64), DeliveryRoot: delivery})), 0600)
	pkg := filepath.Join(delivery, "install", "package")
	m := dto.EnginePluginManifest{PluginAPIVersion: 2, ID: "fixture", Name: "Fixture", Description: "test", Version: "1.0.0", Platform: dto.PlatformTarget{Os: runtime.GOOS, Arch: runtime.GOARCH}}
	m.Desktop.BundleTopLevelDir = m.ID
	m.Requires.PlatformAgentEngineAPI = 2
	m.Requires.WebclientAgentEngineAPI = 1
	m.Resources.AgentEngines = []dto.EngineResource{{ID: "agent", Name: "Agent", Protocol: "acp", Transport: "stdio", Launch: dto.EngineLaunch{Runtime: "node", Entry: "agent.cjs", Args: []string{}}, Adapter: &dto.AdapterVersion{Package: "@agentclientprotocol/codex-acp", Version: "1.0.0", ACPSDKVersion: "2.0.2"}, Dependencies: []dto.EngineDependency{{ID: "node", Version: strings.TrimSpace(string(version)), Delivery: "host-managed"}}}}
	writeProjectEngineFile(t, filepath.Join(pkg, "manifest.json"), string(encodeProjectEngine(t, m)), 0600)
	writeProjectEngineFile(t, filepath.Join(pkg, "agent.cjs"), script, 0600)
	writeProjectEngineFile(t, filepath.Join(pkg, "node_modules/@agentclientprotocol/codex-acp/package.json"), `{"name":"@agentclientprotocol/codex-acp","version":"1.0.0"}`, 0600)
	writeProjectEngineFile(t, filepath.Join(pkg, "node_modules/@agentclientprotocol/sdk/package.json"), `{"name":"@agentclientprotocol/sdk","version":"2.0.2"}`, 0600)
	digest, err := connector.RuntimeFingerprint(pkg)
	if err != nil {
		t.Fatal(err)
	}
	record := dto.TrustedDeliveryRecord{SchemaVersion: 1, InstallationRef: "install", OwnerHostID: "host", PluginID: m.ID, PluginVersion: m.Version, Platform: m.Platform, ArtifactDigest: "sha256:" + digest, Source: dto.DeliverySource{Kind: "local-import", AuthorizationRef: "local-import:install", ArchiveDigest: "sha256:" + strings.Repeat("b", 64)}}
	writeProjectEngineFile(t, filepath.Join(delivery, "install", "record.json"), string(encodeProjectEngine(t, record)), 0600)
	registry, err := agentengine.New(filepath.Join(root, "runtime"), boot, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.Close() })
	_, err = registry.Apply("snapshot", encodeProjectEngine(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{{EngineID: "fixture/agent", PluginID: m.ID, PluginVersion: m.Version, InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	return registry, root
}

func TestACPPluginProjectCreationAndLiveDirectory(t *testing.T) {
	engines, root := projectEngineFixture(t, `require('node:fs').writeFileSync(process.cwd()+'/adapter-started', 'unexpected')`)
	fixture := newTestFixture(t)
	fixture.server.deps.AgentEngines = engines
	directory := func() api.AgentCreationTypeOption {
		t.Helper()
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/agents/creation-defaults", nil))
		var response api.ApiResponse[api.AgentCreationDefaultsResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		for _, option := range response.Data.Types {
			if option.Key == "acp" {
				return option
			}
		}
		t.Fatal("missing ACP option")
		return api.AgentCreationTypeOption{}
	}
	option := directory()
	if !option.Available || option.Mode != "" || option.ModelRequired || option.EngineDirectory == nil || option.EngineDirectory.Engines[0].AuthState != "unknown" {
		t.Fatalf("unexpected ACP option: %#v", option)
	}
	definition := map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"engineId": "fixture/agent", "workspaceRoot": root}}
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{"isProject": true, "definition": definition})
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mode:", "modelConfig:", "budget:", "acpBridgeId:"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("native/legacy field leaked: %s", data)
		}
	}
	if !strings.Contains(string(data), "engineId: fixture/agent") {
		t.Fatal(string(data))
	}
	def, ok := fixture.server.deps.Registry.AgentDefinition(created.Key)
	if !ok || def.Mode != "" || def.ModelKey != "" || len(def.Budget) > 0 || def.Interaction().Model || def.Interaction().DefaultDetail != "simple" {
		t.Fatalf("unexpected runtime definition: %#v", def)
	}
	// A native model endpoint must not probe or persist external configuration.
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agent/model-config", bytes.NewReader(encodeProjectEngine(t, map[string]any{"agentKey": created.Key, "modelKey": "mock-model"}))))
	if rec.Code != 400 {
		t.Fatalf("model override accepted: %d %s", rec.Code, rec.Body)
	}
	options := fixture.server.buildModelOptionsForAgent(created.Key)
	if len(options.Models) != 0 || len(options.ReasoningEfforts) != 0 || len(options.ServiceTiers) != 0 {
		t.Fatalf("native options leaked: %#v", options)
	}
	recordData, err := os.ReadFile(filepath.Join(root, "desktop/data/agent-engines/install/record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record dto.TrustedDeliveryRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	disabled := dto.EngineInstallation{EngineID: "fixture/agent", PluginID: "fixture", PluginVersion: "1.0.0", InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: false}
	for index, entries := range [][]dto.EngineInstallation{{disabled}, {}} {
		if _, err := engines.Apply("snapshot", encodeProjectEngine(t, dto.EngineSnapshot{APIVersion: 1, Revision: int64(index + 2), Entries: entries})); err != nil {
			t.Fatal(err)
		}
		if directory().Available {
			t.Fatal("disabled/removed plugin still available")
		}
		rec = httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("POST", "/api/admin/agents/create", bytes.NewReader(encodeProjectEngine(t, map[string]any{"isProject": true, "definition": definition}))))
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "engine_unavailable") {
			t.Fatalf("stale selection accepted: %d %s", rec.Code, rec.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "adapter-started")); !os.IsNotExist(err) {
		t.Fatal("creation or discovery launched adapter")
	}
}
