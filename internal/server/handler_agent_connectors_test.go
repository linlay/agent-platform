package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func agentConnectorsFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		for _, id := range []string{"docs", "meeting", "mail"} {
			writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), id)
		}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(strings.Replace(string(content), "  modelKey: mock-model", "  modelKey: mock-model\n  reasoning:\n    effort: low", 1))
		if err := os.WriteFile(path, append(content, []byte("\nconnectorConfig:\n  connectors:\n    - docs\n    - meeting\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
	}})
}

func agentConnectorRequest(server *Server, method, key string, payload any) *httptest.ResponseRecorder {
	var body bytes.Buffer
	if payload != nil {
		_ = json.NewEncoder(&body).Encode(payload)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(method, "/api/admin/agents/connectors?agentKey="+key, &body))
	return rec
}

func agentConnectorResponse(t *testing.T, rec *httptest.ResponseRecorder) api.AgentConnectorsResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data api.AgentConnectorsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestAgentConnectorsReadSourceAndPersistSingleToggle(t *testing.T) {
	f := agentConnectorsFixture(t)
	before := getAdminAgentDetail(t, f.server, "mock-agent")
	initial := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if !reflect.DeepEqual(initial.ConnectorIDs, []string{"docs", "meeting"}) || initial.ReloadPending {
		t.Fatalf("initial: %#v", initial)
	}
	updated := agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "mail", "enabled": true}))
	if !reflect.DeepEqual(updated.ConnectorIDs, []string{"docs", "meeting", "mail"}) || updated.ReloadPending {
		t.Fatalf("updated: %#v", updated)
	}
	after := getAdminAgentDetail(t, f.server, "mock-agent")
	source, err := f.registry.(*catalog.FileRegistry).ReadEditableAgentSource("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source.Content, "effort: low") {
		t.Fatal("connector edit normalized unrelated model settings")
	}
	after.Definition["connectorConfig"] = before.Definition["connectorConfig"]
	if !reflect.DeepEqual(before.Definition, after.Definition) || before.SoulPrompt != after.SoulPrompt || before.AgentsPrompt != after.AgentsPrompt {
		t.Fatal("unrelated agent fields changed")
	}
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "docs", "enabled": false}))
	reopened := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if !reflect.DeepEqual(reopened.ConnectorIDs, []string{"meeting", "mail"}) {
		t.Fatalf("reopened: %#v", reopened)
	}
	recorder := &recordingServerCatalogReloader{}
	f.server.deps.CatalogReloader = recorder
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "mail", "enabled": true}))
	if len(recorder.reasons) != 0 {
		t.Fatal("idempotent toggle reloaded agent")
	}
}

func TestAgentConnectorsReportConfiguredStateWhileRuntimeLeased(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Errorf("reload after lease: %v", err)
		}
	})
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing agent")
	}
	t.Cleanup(release)
	updated := agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "docs", "enabled": false}))
	if !updated.ReloadPending || !reflect.DeepEqual(updated.ConnectorIDs, []string{"meeting"}) || !reflect.DeepEqual(updated.ActiveConnectorIDs, []string{"docs", "meeting"}) {
		t.Fatalf("leased update: %#v", updated)
	}
	read := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if !reflect.DeepEqual(read.ConnectorIDs, updated.ConnectorIDs) {
		t.Fatal("GET returned active rather than configured state")
	}
	release()
	read = agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if read.ReloadPending {
		t.Fatal("agent did not reload after releasing runtime lease")
	}
}

func TestAgentConnectorsRejectInvalidEditsAndRestoreOnReloadFailure(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, _ := registry.ReadEditableAgentSource("mock-agent")
	for _, payload := range []map[string]any{
		{"agentKey": "mock-agent", "connectorId": "mail"},
		{"agentKey": "mock-agent", "connectorId": "../mail", "enabled": true},
		{"agentKey": "mock-agent", "connectorId": "missing", "enabled": true},
		{"agentKey": "missing", "connectorId": "mail", "enabled": true},
	} {
		rec := agentConnectorRequest(f.server, "PUT", "", payload)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("expected rejection, got %d: %s", rec.Code, rec.Body.String())
		}
	}
	if rec := agentConnectorRequest(f.server, "GET", "", nil); rec.Code != 400 {
		t.Fatalf("missing key: %d", rec.Code)
	}
	f.server.deps.CatalogReloader = &recordingServerCatalogReloader{err: errors.New("reload failed")}
	rec := agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "mail", "enabled": true})
	if rec.Code != 500 {
		t.Fatalf("expected reload failure, got %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := registry.ReadEditableAgentSource("mock-agent")
	if before.Content != after.Content {
		t.Fatal("failed edits changed original YAML")
	}
}

func TestAgentConnectorsConcurrentTogglesPreserveBothChanges(t *testing.T) {
	f := agentConnectorsFixture(t)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, id := range []string{"docs", "meeting"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			responses <- agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": id, "enabled": false})
		}(id)
	}
	wg.Wait()
	close(responses)
	for rec := range responses {
		agentConnectorResponse(t, rec)
	}
	read := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if len(read.ConnectorIDs) != 0 || read.ConnectorIDs == nil {
		t.Fatalf("lost update or null empty list: %#v", read)
	}
}
