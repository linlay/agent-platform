package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

func TestConnectorDeleteOverHTTPAndReimportRequiresNewAuthorization(t *testing.T) {
	f := agentConnectorsFixture(t)
	endpoint := httptest.NewServer(f.server)
	defer endpoint.Close()
	const id = "delete-smoke"
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	for name, value := range map[string]string{
		"connector.json":    `{"id":"delete-smoke","name":"Delete smoke","version":"1.0.0","type":"mcp","authMode":"token"}`,
		"token-schema.json": `{"fields":[{"key":"API_KEY","type":"password","label":"API Key","required":true}]}`,
		"mcp.json":          `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp","headers":{"X-API-Key":"${API_KEY}"}}}}`,
	} {
		file, err := zipWriter.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	importPackage := func() {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", id+".zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(archive.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		response, err := endpoint.Client().Post(endpoint.URL+"/api/admin/connectors/import", form.FormDataContentType(), &body)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte(`"installed":true`)) {
			t.Fatalf("import failed: %d %s", response.StatusCode, payload)
		}
	}
	importPackage()
	sources := f.server.connectorSources()
	state := filepath.Join(sources.PersistentRoot(), id)
	for name, value := range map[string]string{
		"credentials.json": `{"API_KEY":"test-only-old-account"}`,
		"connection.json":  `{"connectorId":"delete-smoke","configured":true}`,
		"config/cli.json":  `{"account":"test-only"}`,
	} {
		file := filepath.Join(state, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(http.MethodDelete, endpoint.URL+"/api/admin/connectors/detail?id="+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := endpoint.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte(`"deleted":true`)) {
		t.Fatalf("delete failed: %d %s", response.StatusCode, payload)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("HTTP delete retained account state: %v", err)
	}
	importPackage()
	response, err = endpoint.Client().Get(endpoint.URL + "/api/admin/connectors/auth?id=" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ = io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte(`"status":"unauthorized"`)) {
		t.Fatalf("re-import reused authorization: %d %s", response.StatusCode, payload)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("authorization polling recreated private state: %v", err)
	}
}

func deleteConnectorRequest(s *Server, id string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/connectors/detail?id="+id, nil))
	return rec
}

func TestConnectorDeleteHTTPContractAndRollback(t *testing.T) {
	f := agentConnectorsFixture(t)
	sources := f.server.connectorSources()
	root := sources.ExternalRoot
	state := filepath.Join(sources.PersistentRoot(), "mail")
	installation, err := connector.InstallDir(sources.InstallRoot(), "mail")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(state, "credentials.json"), filepath.Join(state, "oauth.json"),
		filepath.Join(state, "config", "settings.json"), filepath.Join(state, "cache", "cache.json"),
		filepath.Join(state, "data", "data.json"), filepath.Join(installation, "preparation.json"),
	}
	keep := filepath.Join(sources.PersistentRoot(), "docs", "credentials.json")
	for _, file := range append(files, keep) {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(`{"fixture":"private"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry := f.registry.(*catalog.FileRegistry)
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing Agent")
	}
	defer release()
	for id, status := range map[string]int{"builtin.dbx": 403, "missing": 404, "../bad": 400, "": 400, "docs": 409} {
		rec := deleteConnectorRequest(f.server, id)
		if rec.Code != status {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
		if status == 409 && !strings.Contains(rec.Body.String(), `"agentKeys":["mock-agent"]`) {
			t.Fatal(rec.Body.String())
		}
		if status == 409 {
			var body struct {
				Data struct {
					Error struct {
						Code      string   `json:"code"`
						Message   string   `json:"message"`
						AgentKeys []string `json:"agentKeys"`
					} `json:"error"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.Error.Code != "connector_in_use" || !strings.Contains(body.Data.Error.Message, "mock-agent") || len(body.Data.Error.AgentKeys) != 1 {
				t.Fatal(rec.Body.String())
			}
		}
	}
	reloader := &recordingServerCatalogReloader{err: errServerCatalogReload}
	f.server.deps.CatalogReloader = reloader
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 500 {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "mail", "connector.json")); err != nil {
		t.Fatal("rollback lost package", err)
	}
	for _, file := range files {
		if data, err := os.ReadFile(file); err != nil || string(data) != `{"fixture":"private"}` {
			t.Fatalf("rollback lost private state or CLI installation %s: %v", file, err)
		}
	}
	reloader.err = nil
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	for _, target := range []string{filepath.Join(root, "mail"), state, installation} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("HTTP deletion retained target %s: %v", target, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("deletion removed another connector's credentials", err)
	}
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 404 {
		t.Fatalf("repeat: %d", rec.Code)
	}
}

func TestConnectorDeleteCleanupFailureHasRetryableErrorCode(t *testing.T) {
	f := agentConnectorsFixture(t)
	rec := httptest.NewRecorder()
	f.server.writeConnectorError(rec, connector.ErrDeleteCleanup)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"code":"connector_cleanup_pending"`) || strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("incomplete cleanup was reported as successful: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorDeleteChecksFreshInvalidSourcesAndLeasedRuntimes(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	path := filepath.Join(f.server.deps.Config.Paths.AgentsDir, "new-invalid.yml")
	// The Agent is not in the loaded catalog and lacks required model settings.
	if err := os.WriteFile(path, []byte("key: invalid\nconnectorConfig:\n  connectors:\n    - mail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "invalid") {
		t.Fatalf("fresh source: %d %s", rec.Code, rec.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Error(err)
		}
	})
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing Agent")
	}
	t.Cleanup(release)
	response := agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "docs", "enabled": false}))
	if response.ReloadPending {
		t.Fatal("new runtime must publish while old lease retains connector")
	}
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 409 {
		t.Fatalf("leased: %d %s", rec.Code, rec.Body.String())
	}
	release()
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 200 {
		t.Fatalf("released: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.server.connectorSources().Load("docs"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package still installed: %v", err)
	}
}

func TestConnectorDeleteAutomaticallyDetachesSavedMounts(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	// A second Agent references the same package, but no Run holds a lease.
	second := strings.ReplaceAll(before.Content, "mock-agent", "second-agent")
	if err := os.MkdirAll(filepath.Join(f.cfg.Paths.AgentsDir, "second-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.cfg.Paths.AgentsDir, "second-agent", "agent.yml"), []byte(second), 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	f.server.ServeHTTP(list, httptest.NewRequest("GET", "/api/admin/connectors", nil))
	var body struct {
		Data struct {
			Connectors []struct {
				ID    string `json:"id"`
				Count *int   `json:"mountedAgentCount"`
			} `json:"connectors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range body.Data.Connectors {
		if item.ID == "docs" {
			found = true
			if item.Count == nil || *item.Count != 2 {
				t.Fatalf("count: %#v", item.Count)
			}
		}
	}
	if !found {
		t.Fatal("connector missing from directory")
	}
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"mock-agent", "second-agent"} {
		ids, err := registry.ReadAgentConnectors(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != "meeting" {
			t.Fatalf("%s mounts: %v", key, ids)
		}
		def, ok := registry.AgentDefinition(key)
		if !ok {
			t.Fatalf("Agent %s disappeared", key)
		}
		for _, id := range def.Connectors {
			if id == "docs" {
				t.Fatal("stale published mount")
			}
		}
	}
	after, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.Content, "effort: low") {
		t.Fatal("unrelated settings changed")
	}
	if _, err := f.server.connectorSources().Load("docs"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package remains: %v", err)
	}
}

func TestConnectorDeleteRunStartedAfterConfirmationLeavesMountsUntouched(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the user opening confirmation before the lease is acquired.
	if users, err := registry.ActiveConnectorUsers("docs"); err != nil || len(users) != 0 {
		t.Fatalf("preflight: %v %v", users, err)
	}
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing runtime")
	}
	defer release()
	rec := deleteConnectorRequest(f.server, "docs")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "connector_in_use") {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body.String())
	}
	after, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	if before.Content != after.Content {
		t.Fatal("busy deletion changed mounts")
	}
	if users, err := registry.ActiveConnectorUsers("docs"); err != nil || len(users) != 1 {
		t.Fatalf("lease was changed: %v %v", users, err)
	}
	release()
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 200 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorDeleteRestoresMountsWhenReloadFails(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	f.server.deps.CatalogReloader = &recordingServerCatalogReloader{err: errors.New("reload failed")}
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 500 {
		t.Fatalf("reload failure: %d %s", rec.Code, rec.Body.String())
	}
	after, err := registry.ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	if before.Content != after.Content {
		t.Fatal("mounts not restored")
	}
	if _, err := f.server.connectorSources().Load("docs"); err != nil {
		t.Fatalf("package not restored: %v", err)
	}
	// Failed deletion must release its execution-admission gate.
	if _, release, ok := registry.AcquireAgentRuntime("mock-agent"); !ok {
		t.Fatal("gate retained after rollback")
	} else {
		release()
	}
}

func TestConnectorDeleteCatalogControlSharesDetachAndBusyPolicy(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	service := &adminsource.ControlService{Config: f.cfg, Registry: registry, Mutations: f.server.adminSources, BeginConnectorDelete: f.server.connectorAuth.BeginDelete, Reload: func(ctx context.Context, reason string) error {
		return f.server.deps.CatalogReloader.Reload(ctx, reason)
	}}
	target := adminsource.ControlTarget{ResourceType: "connector", ResourceKey: "docs"}
	source, err := service.Read(target)
	if err != nil {
		t.Fatal(err)
	}
	change := adminsource.ControlChange{ControlTarget: target, Action: "delete", BaseRevision: source.BaseRevision}
	plan, err := service.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DetachedAgentKeys) != 1 || plan.DetachedAgentKeys[0] != "mock-agent" {
		t.Fatalf("approval: %#v", plan.DetachedAgentKeys)
	}
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing runtime")
	}
	defer release()
	if _, err := service.Apply(context.Background(), change, "caller", plan.Digest); err == nil {
		t.Fatal("tool deletion bypassed live lease")
	}
	release()
	root := f.server.connectorSources().PersistentRoot()
	state := filepath.Join(root, "docs", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(`{"fixture":"private"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), change, "caller", plan.Digest)
	if err != nil || result["status"] != "applied" {
		t.Fatalf("apply: %v %v", result, err)
	}
	ids, err := registry.ReadAgentConnectors("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "meeting" {
		t.Fatalf("stale mounts: %v", ids)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("private state retained: %v", err)
	}
}
