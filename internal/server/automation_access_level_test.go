package server

import (
	"agent-platform/internal/automation"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestAutomationAccessLevelHTTPRoundTrip(t *testing.T) {
	fixture := newAutomationTestServer(t, false)
	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name": "Permissions", "cron": "0 9 * * *", "agentKey": "demo-agent", "enabled": false,
		"query": map[string]any{"message": "run", "accessLevel": "auto_approve"},
	})
	if created.Query.AccessLevel != "auto_approve" {
		t.Fatalf("create lost permission: %#v", created.Query)
	}
	unchanged := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/update", map[string]any{"id": created.ID, "name": "Renamed"})
	if unchanged.Query.AccessLevel != "auto_approve" {
		t.Fatal("metadata update changed access level")
	}
	for _, level := range []string{"full_access", "default", ""} {
		query := map[string]any{"message": "updated"}
		if level != "" {
			query["accessLevel"] = level
		}
		updated := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/update", map[string]any{"id": created.ID, "query": query})
		if updated.Query.AccessLevel != level {
			t.Fatalf("update want %q got %#v", level, updated.Query)
		}
		loaded := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation", map[string]any{"id": created.ID})
		if loaded.Query.AccessLevel != level {
			t.Fatalf("reload lost permission: %#v", loaded.Query)
		}
	}
	for _, level := range []any{"unsafe", true, 42} {
		status := postAutomationStatus(t, fixture.server, "/api/automation/update", map[string]any{"id": created.ID, "query": map[string]any{"message": "bad", "accessLevel": level}})
		if status != http.StatusBadRequest {
			t.Fatalf("accepted %v: HTTP %d", level, status)
		}
	}
	loaded := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation", map[string]any{"id": created.ID})
	if loaded.Query.AccessLevel != "" || loaded.Query.Message != "updated" {
		t.Fatalf("invalid update changed definition: %#v", loaded.Query)
	}
}

func TestAutomationAccessLevelReachesRuntime(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{})
	// Automation uses Query admission; it is independent of chat_start permission review.
	var chatID string
	for _, level := range []string{"full_access", "auto_approve", "", "default"} {
		def := automation.Definition{ID: "scheduled", AgentKey: "mock-agent", Query: automation.Query{ChatID: chatID, Message: "run task", AccessLevel: level}}
		result, err := fixture.server.ExecuteQuery(context.Background(), queryCommandFromAPI(def.ToQueryRequest()), runtimetypes.QueryHooks{})
		if err != nil || result.Completion == nil {
			t.Fatalf("execute level=%q: %#v %v", level, result, err)
		}
		snapshot, err := fixture.server.GetRunStatus(result.Completion.RunID)
		expected, _ := contracts.NormalizeAccessLevel(level)
		if err != nil || snapshot.AccessLevel != expected {
			t.Fatalf("want %s, got %#v: %v", expected, snapshot, err)
		}
		chatID = result.Completion.ChatID
	}
}

func TestAutomationAccessLevelRespectsTargetAdmission(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected automation must not call the provider")
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(content, []byte("\ninteractionConfig:\n  accessLevel: false\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	def := automation.Definition{ID: "scheduled", AgentKey: "mock-agent", Query: automation.Query{Message: "run task", AccessLevel: "full_access"}}
	_, err := fixture.server.ExecuteQuery(context.Background(), queryCommandFromAPI(def.ToQueryRequest()), runtimetypes.QueryHooks{})
	if err == nil || !strings.Contains(err.Error(), "interactionConfig.accessLevel") {
		t.Fatalf("expected target rejection, got %v", err)
	}
}
