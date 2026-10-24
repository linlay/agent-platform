package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/viewquery"
)

func TestViewQueryRunsDeclaredBuiltinQueryForTheViewer(t *testing.T) {
	var calls atomic.Int32
	systemPrompt := make(chan string, 1)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			body, _ := io.ReadAll(r.Body)
			systemPrompt <- string(body)
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "```view key=builtin.task-control/automations query=list\n{\"limit\":5}\n```\n"}, "finish_reason": "stop"}}})
		writeProviderSSE(t, w, string(chunk), `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Prompts.View = config.PromptViewConfig{InstructionsPrompt: "VIEW RULES", CatalogHeader: "VIEWS:"}
		},
		setupRuntime: func(root string, cfg *config.Config) {
			cfg.Paths.BuiltinConnectorsDir = filepath.Join(root, "builtin-connectors")
			release, err := cfg.Paths.PrepareNativeConnectors()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - builtin.task-control\n")...)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
	var seen viewquery.Context
	fixture.server.deps.ViewQueries = &viewquery.Service{Operations: map[string]viewquery.Operation{
		"automation.list": func(_ context.Context, qc viewquery.Context, params map[string]any) (any, error) {
			seen = qc
			return map[string]any{"items": []any{map[string]any{"id": "daily"}}, "total": 1, "limit": params["limit"]}, nil
		},
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"list automations","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	var chatID, runID, hash string
	for _, message := range decodeSSEMessages(t, rec.Body.String()) {
		switch message["type"] {
		case "run.start":
			chatID, runID = message["chatId"].(string), message["runId"].(string)
		case "content.view":
			if message["viewError"] != nil {
				t.Fatalf("binding failed: %#v", message)
			}
			hash = message["view"].(map[string]any)["hash"].(string)
		}
	}
	if hash == "" || runID == "" {
		t.Fatalf("no binding: %s", rec.Body.String())
	}
	prompt := <-systemPrompt
	if !strings.Contains(prompt, "key: builtin.task-control/automations") || !strings.Contains(prompt, "query list: List automations.") {
		t.Fatalf("builtin view missing from catalog prompt: %s", prompt)
	}

	post := func(body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/view/query", bytes.NewReader(data)))
		var response map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &response)
		return rec, response
	}
	ref := map[string]any{"connectorId": "builtin.task-control", "key": "automations", "hash": hash}
	rec, response := post(map[string]any{"chatId": chatID, "runId": runID, "view": ref, "query": "list", "params": map[string]any{"limit": 5}})
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	body := response["data"].(map[string]any)
	if executedAt, _ := body["executedAt"].(float64); executedAt <= 0 {
		t.Fatalf("executedAt missing: %#v", body)
	}
	result := body["data"].(map[string]any)
	if result["total"] != float64(1) || result["limit"] != float64(5) {
		t.Fatalf("result: %#v", result)
	}
	if seen.ChatID != chatID || seen.RunID != runID || seen.AgentKey != "mock-agent" {
		t.Fatalf("query context: %#v", seen)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		code int
		want string
	}{
		"schema":       {map[string]any{"chatId": chatID, "runId": runID, "view": ref, "query": "list", "params": map[string]any{"limit": 500}}, 400, "view_query_params_invalid"},
		"undeclared":   {map[string]any{"chatId": chatID, "runId": runID, "view": ref, "query": "delete"}, 404, "view_not_found"},
		"foreign run":  {map[string]any{"chatId": chatID, "runId": "other-run", "view": ref, "query": "list"}, 404, "view_query_run_not_found"},
		"no hash":      {map[string]any{"chatId": chatID, "runId": runID, "view": map[string]any{"connectorId": "builtin.task-control", "key": "automations"}, "query": "list"}, 400, "invalid_view"},
		"unknown key":  {map[string]any{"chatId": chatID, "runId": runID, "view": ref, "query": "list", "operation": "catalog.list"}, 400, "invalid_view"},
		"wrong target": {map[string]any{"chatId": chatID, "runId": runID, "view": map[string]any{"connectorId": "builtin.platform-control", "key": "automations", "hash": hash}, "query": "list"}, 400, "invalid_view"},
	} {
		rec, _ := post(tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// Connector failures keep their code but never reach the client as 401/403,
// which would read as the viewer's own Platform session failing.
func TestViewQueryConnectorFailuresKeepCodeWithoutAuthStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&connectorops.Error{Code: "connector_auth_required", Status: http.StatusUnauthorized}, http.StatusConflict, "connector_auth_required"},
		{&connectorops.Error{Code: "connector_execution_not_allowed", Status: http.StatusForbidden}, http.StatusConflict, "connector_execution_not_allowed"},
		{fmt.Errorf("wrapped: %w", &connectorops.Error{Code: "invocation_timeout", Status: http.StatusGatewayTimeout}), http.StatusGatewayTimeout, "invocation_timeout"},
	} {
		status, code, _ := viewQueryStatus(tc.err)
		if status != tc.status || code != tc.code {
			t.Fatalf("%v: %d %s", tc.err, status, code)
		}
	}
}
