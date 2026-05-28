package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func setupAgentOrderFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			for _, key := range []string{"alpha-agent", "zulu-agent"} {
				agentDir := filepath.Join(cfg.Paths.AgentsDir, key)
				if err := os.MkdirAll(agentDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", key, err)
				}
				content := strings.Join([]string{
					"key: " + key,
					"name: " + key,
					"modelConfig:",
					"  modelKey: mock-model",
				}, "\n")
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", key, err)
				}
			}
		},
	})
}

func TestAgentOrderEndpointReadsEmptyWhenMissingAndWritesOrder(t *testing.T) {
	fixture := setupAgentOrderFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("read order status = %d body=%s", rec.Code, rec.Body.String())
	}
	var readResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &readResp); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	if readResp.Data.Version != 1 || readResp.Data.UpdatedAt != 0 || len(readResp.Data.Order) != 0 {
		t.Fatalf("unexpected empty order response: %#v", readResp.Data)
	}

	body := bytes.NewBufferString(`{"order":["zulu-agent","mock-agent"]}`)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/agents/order", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("write order status = %d body=%s", rec.Code, rec.Body.String())
	}
	var writeResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	if !reflect.DeepEqual(writeResp.Data.Order, []string{"zulu-agent", "mock-agent"}) || writeResp.Data.UpdatedAt <= 0 {
		t.Fatalf("unexpected write response: %#v", writeResp.Data)
	}

	data, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.AgentsDir, catalog.AgentOrderFileName))
	if err != nil {
		t.Fatalf("read persisted order: %v", err)
	}
	if !strings.Contains(string(data), `"zulu-agent"`) || !strings.Contains(string(data), `"mock-agent"`) {
		t.Fatalf("persisted order missing keys: %s", data)
	}
}

func TestAgentOrderEndpointRejectsInvalidOrder(t *testing.T) {
	fixture := setupAgentOrderFixture(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty key", body: `{"order":["mock-agent"," "]}`},
		{name: "duplicate", body: `{"order":["mock-agent","mock-agent"]}`},
		{name: "unknown", body: `{"order":["missing-agent"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/agents/order", bytes.NewBufferString(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAgentsEndpointUsesFixedOrderWithChats(t *testing.T) {
	fixture := setupAgentOrderFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.AgentsDir, catalog.AgentOrderFileName), []byte(`{
  "version": 1,
  "order": ["zulu-agent", "mock-agent"],
  "updatedAt": 1000
}`), 0o644); err != nil {
		t.Fatalf("write order: %v", err)
	}
	if err := fixture.registry.Reload(t.Context(), "agents"); err != nil {
		t.Fatalf("reload agents: %v", err)
	}
	if _, _, err := fixture.chats.EnsureChat("chat-mock", "mock-agent", "", "mock"); err != nil {
		t.Fatalf("ensure mock chat: %v", err)
	}
	if err := fixture.chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-mock", RunID: "zzzz", UpdatedAtMillis: 9000}); err != nil {
		t.Fatalf("complete mock chat: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?includeChats=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents status = %d body=%s", rec.Code, rec.Body.String())
	}
	var agentsResp api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	keys := make([]string, 0, len(agentsResp.Data))
	for _, item := range agentsResp.Data {
		keys = append(keys, item.Key)
	}
	if !reflect.DeepEqual(keys, []string{"zulu-agent", "mock-agent", "alpha-agent"}) {
		t.Fatalf("agent keys = %#v", keys)
	}
	if len(agentsResp.Data[1].Chats) != 1 || agentsResp.Data[1].Chats[0].LastRunID != "zzzz" {
		t.Fatalf("expected mock chat attached without changing order, got %#v", agentsResp.Data[1].Chats)
	}
}
