package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func setupAgentOrderFixture(t *testing.T, notifications contracts.NotificationSink) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}, testFixtureOptions{
		notifications: notifications,
		setupRuntime: func(_ string, cfg *config.Config) {
			writeInvalidAdminAgentFixtures(t, cfg)
			writeAgentOrderTestAgent(t, cfg, "agent-b", nil)
			writeAgentOrderTestAgent(t, cfg, "hidden-agent", []string{"internal"})
			order := catalog.AgentOrderFile{
				Version: 1,
				Order: []string{
					"agent-b",
					"invalid-yaml",
					"mock-agent",
					"invalid-semantic",
					"hidden-agent",
				},
				UpdatedAt: testEpochMillis,
			}
			data, err := json.Marshal(order)
			if err != nil {
				t.Fatalf("marshal initial agent order: %v", err)
			}
			if err := os.WriteFile(catalog.AgentOrderPath(cfg.Paths.AgentsDir), data, 0o644); err != nil {
				t.Fatalf("write initial agent order: %v", err)
			}
		},
	})
}

func writeAgentOrderTestAgent(t *testing.T, cfg *config.Config, key string, scopes []string) {
	t.Helper()
	dir := filepath.Join(cfg.Paths.AgentsDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", key, err)
	}
	lines := []string{
		"key: " + key,
		"name: " + key,
		"mode: REACT",
		"modelConfig:",
		"  modelKey: mock-model",
	}
	if len(scopes) > 0 {
		lines = append(lines, "visibility:", "  scopes:")
		for _, scope := range scopes {
			lines = append(lines, "    - "+scope)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

func TestPublicAgentOrderReturnsAllValidAgentsAndPreservesInvalidSlots(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := setupAgentOrderFixture(t, notifications)

	read := httptest.NewRecorder()
	fixture.server.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/agents/order?scope=nav&mode=CODER", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("public order read status = %d body=%s", read.Code, read.Body.String())
	}
	var readResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(read.Body.Bytes(), &readResp); err != nil {
		t.Fatalf("decode public order read: %v", err)
	}
	if !reflect.DeepEqual(readResp.Data.Order, []string{"agent-b", "mock-agent", "hidden-agent"}) {
		t.Fatalf("public order filtered or exposed invalid agents: %#v", readResp.Data.Order)
	}
	if readResp.Data.UpdatedAt == nil || *readResp.Data.UpdatedAt != testEpochMillis {
		t.Fatalf("unexpected public order updatedAt: %#v", readResp.Data.UpdatedAt)
	}

	write := httptest.NewRecorder()
	fixture.server.ServeHTTP(write, httptest.NewRequest(
		http.MethodPut,
		"/api/agents/order",
		bytes.NewBufferString(`{"order":["hidden-agent","mock-agent"]}`),
	))
	if write.Code != http.StatusOK {
		t.Fatalf("public order write status = %d body=%s", write.Code, write.Body.String())
	}
	var writeResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(write.Body.Bytes(), &writeResp); err != nil {
		t.Fatalf("decode public order write: %v", err)
	}
	if !reflect.DeepEqual(writeResp.Data.Order, []string{"hidden-agent", "mock-agent", "agent-b"}) {
		t.Fatalf("missing concurrent agent was not appended: %#v", writeResp.Data.Order)
	}

	persisted, err := catalog.ReadAgentOrderFile(fixture.cfg.Paths.AgentsDir)
	if err != nil {
		t.Fatalf("read persisted agent order: %v", err)
	}
	wantPersisted := []string{"hidden-agent", "invalid-yaml", "mock-agent", "invalid-semantic", "agent-b"}
	if !reflect.DeepEqual(persisted.Order, wantPersisted) {
		t.Fatalf("invalid agent slots changed: got %#v want %#v", persisted.Order, wantPersisted)
	}
	if events := notifications.EventTypes(); !reflect.DeepEqual(events, []string{"catalog.updated"}) {
		t.Fatalf("expected one catalog.updated broadcast, got %#v", events)
	}

	if err := fixture.registry.Reload(t.Context(), "agents"); err != nil {
		t.Fatalf("reload registry after order write: %v", err)
	}
	afterReload := fixture.registry.Agents("all")
	if got := runtimeAgentKeys(afterReload); !reflect.DeepEqual(got, writeResp.Data.Order) {
		t.Fatalf("order did not survive registry reload: %#v", got)
	}

	adminRead := httptest.NewRecorder()
	fixture.server.ServeHTTP(adminRead, httptest.NewRequest(http.MethodGet, "/api/admin/agents/order", nil))
	if adminRead.Code != http.StatusOK {
		t.Fatalf("admin order after public write status = %d body=%s", adminRead.Code, adminRead.Body.String())
	}
	var adminResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(adminRead.Body.Bytes(), &adminResp); err != nil {
		t.Fatalf("decode admin order after public write: %v", err)
	}
	if !reflect.DeepEqual(adminResp.Data.Order, wantPersisted) {
		t.Fatalf("admin order did not converge with public write: %#v", adminResp.Data.Order)
	}
}

func TestRuntimeAgentOrderDoesNotFilterVisibilityOrMode(t *testing.T) {
	items := []api.AgentSummary{
		{Key: "react", Mode: "REACT"},
		{Key: "hidden", Mode: "REACT"},
		{Key: "coder", Mode: "CODER"},
		{Key: "kbase", Mode: "KBASE"},
	}
	if got := runtimeAgentKeys(items); !reflect.DeepEqual(got, []string{"react", "hidden", "coder", "kbase"}) {
		t.Fatalf("runtime order filtered valid catalog entries: %#v", got)
	}
}

func TestPublicAgentOrderOmitsUpdatedAtBeforeTheOrderFileExists(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh public order status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fresh public order: %v", err)
	}
	if response.Data.UpdatedAt != nil {
		t.Fatalf("missing order file must omit updatedAt, got %#v", response.Data)
	}
	if !reflect.DeepEqual(response.Data.Order, []string{"mock-agent"}) {
		t.Fatalf("fresh public order must still expose valid agents: %#v", response.Data.Order)
	}
}

func TestPublicAgentOrderRejectsInvalidRequestsWithoutChangingFile(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing order", body: `{}`},
		{name: "empty key", body: `{"order":["mock-agent",""]}`},
		{name: "duplicate key", body: `{"order":["mock-agent","mock-agent"]}`},
		{name: "unknown or deleted key", body: `{"order":["deleted-agent"]}`},
		{name: "invalid admin agent", body: `{"order":["invalid-yaml"]}`},
		{name: "too many keys", body: oversizedAgentOrderBody()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupAgentOrderFixture(t, nil)
			path := catalog.AgentOrderPath(fixture.cfg.Paths.AgentsDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read order before request: %v", err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/agents/order", bytes.NewBufferString(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read order after request: %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected request changed agent-order.json")
			}
		})
	}
}

func oversizedAgentOrderBody() string {
	order := make([]string, maxPublicAgentOrderItems+1)
	for index := range order {
		order[index] = fmt.Sprintf("agent-%d", index)
	}
	payload, _ := json.Marshal(api.UpdateAgentOrderRequest{Order: order})
	return string(payload)
}

func TestAdminAgentOrderChangesAreVisibleThroughPublicOrder(t *testing.T) {
	fixture := setupAgentOrderFixture(t, nil)
	adminWrite := httptest.NewRecorder()
	fixture.server.ServeHTTP(adminWrite, httptest.NewRequest(
		http.MethodPut,
		"/api/admin/agents/order",
		bytes.NewBufferString(`{"order":["invalid-semantic","hidden-agent","agent-b","invalid-yaml","mock-agent"]}`),
	))
	if adminWrite.Code != http.StatusOK {
		t.Fatalf("admin order write status = %d body=%s", adminWrite.Code, adminWrite.Body.String())
	}

	publicRead := httptest.NewRecorder()
	fixture.server.ServeHTTP(publicRead, httptest.NewRequest(http.MethodGet, "/api/agents/order", nil))
	if publicRead.Code != http.StatusOK {
		t.Fatalf("public order after admin write status = %d body=%s", publicRead.Code, publicRead.Body.String())
	}
	var response api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(publicRead.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode public order after admin write: %v", err)
	}
	if !reflect.DeepEqual(response.Data.Order, []string{"hidden-agent", "agent-b", "mock-agent"}) {
		t.Fatalf("public order did not converge with admin write: %#v", response.Data.Order)
	}
}
