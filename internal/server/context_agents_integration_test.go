package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	platformws "agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func newContextCandidatesFixture(t *testing.T, refs []string, calls *atomic.Int32, requests chan<- string) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if requests != nil {
			select {
			case requests <- string(body):
			default:
				t.Error("unexpected extra model request")
			}
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"continued"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		notifications: platformws.NewHub(),
		setupRuntime: func(_ string, cfg *config.Config) {
			write := func(key, extra string) {
				path := filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("key: "+key+"\nmode: GENERAL\nmodelConfig: {modelKey: mock-model}\n"+extra), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// Plain declarations must no longer be rejected for lacking a named connector.
			write("valid-candidate", "description: UNIQUE_VALID_CANDIDATE\ntoolConfig:\n  tools:\n    - desktop_shell\n    - surface_list\n")
			write("invalid-candidate", "description: UNIQUE_INVALID_CANDIDATE\nconnectorConfig:\n  connectors:\n    - missing.connector\n")
			path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, []byte("\ncontextConfig:\n  tags:\n    - agents\n")...)
			if len(refs) > 0 {
				body = append(body, []byte("  agents:\n    - "+strings.Join(refs, "\n    - ")+"\n")...)
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
}

func TestQueryContinuesWithUnavailableContextAgents(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []string
	}{
		{"missing", []string{"missing-candidate"}},
		{"invalid", []string{"invalid-candidate"}},
		{"mixed", []string{"missing-candidate", "valid-candidate", "invalid-candidate"}},
		{"valid", []string{"valid-candidate"}},
		{"all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			requests := make(chan string, 1)
			fixture := newContextCandidatesFixture(t, tc.refs, &calls, requests)
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","message":"hello","stream":false}`)))
			var response api.ApiResponse[api.QueryResponse]
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || response.Code != 0 || response.Data.Content != "continued" || calls.Load() != 1 {
				t.Fatalf("query failed: status=%d body=%s calls=%d", rec.Code, rec.Body.String(), calls.Load())
			}
			prompt := <-requests
			if strings.Contains(prompt, "UNIQUE_VALID_CANDIDATE") || strings.Contains(prompt, "UNIQUE_INVALID_CANDIDATE") || strings.Contains(prompt, "missing-candidate") {
				t.Fatalf("wrong candidate prompt: %s", prompt)
			}
			if strings.Contains(prompt, "Runtime Context: Sub-Agent Candidates") {
				t.Fatal("empty candidate section should be omitted")
			}
			detail, err := fixture.server.adminAgentDetail("mock-agent")
			if err != nil {
				t.Fatal(err)
			}
			wantDiagnostics := 1
			if detail.Status != catalog.AdminAgentStatusReady || len(detail.Diagnostics) != wantDiagnostics {
				t.Fatalf("admin detail=%#v", detail)
			}
			if detail.Diagnostics[0].Severity != "warning" || detail.Diagnostics[0].Code != "context_agents_ignored" {
				t.Fatalf("diagnostics=%#v", detail.Diagnostics)
			}
			for _, key := range []string{"missing-candidate", "invalid-candidate"} {
				// Direct query admission remains strict, even after the parent succeeded.
				rejected := httptest.NewRecorder()
				fixture.server.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"`+key+`","message":"must reject","stream":false}`)))
				if rejected.Code == http.StatusOK || calls.Load() != 1 {
					t.Fatalf("unavailable target reached model: %d %s", rejected.Code, rejected.Body.String())
				}
				// agent_invoke must not obtain execution rights from a context reference.
				engine := &orchestratorAgentEngine{}
				orchestrator := newTestFrameOrchestrator(engine, nil, nil, nil)
				orchestrator.Registry = fixture.registry
				orchestrator.Session.AgentKey = "mock-agent"
				main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{newInvokeAgentsDelta(contracts.SubAgentTaskSpec{SubAgentKey: key, TaskText: "must reject"})}}
				failed, interrupted, err := orchestrator.Run(main)
				if err != nil || failed || interrupted || len(main.injected) != 1 || !main.injected[0].isError || main.injected[0].text != "sub-agent not found: "+key || len(engine.sessions) != 0 {
					t.Fatalf("unexpected child dispatch: %#v err=%v", main.injected, err)
				}
			}
		})
	}
}

func TestWebSocketQueryContinuesWithUnavailableContextAgents(t *testing.T) {
	var calls atomic.Int32
	fixture := newContextCandidatesFixture(t, []string{"invalid-candidate", "valid-candidate"}, &calls, nil)
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sendSelectionLaneRequest(t, conn, "query", "/api/query", map[string]any{"agentKey": "mock-agent", "message": "hello"})
	complete := false
	for {
		var frame platformws.StreamFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != nil && frame.Event.Type == "run.complete" {
			complete = true
		}
		if frame.Reason != "" {
			break
		}
	}
	if !complete || calls.Load() != 1 {
		t.Fatalf("WS query did not complete; model calls=%d", calls.Load())
	}
}

func TestUnavailableContextAgentDoesNotHideRequiredWorkspaceFailure(t *testing.T) {
	cfg := testPromptContextConfig(t)
	server := &Server{deps: Dependencies{Config: cfg, Registry: testCatalogRegistry{}}}
	_, err := server.buildRuntimeRequestContext(runtimeRequestContextInput{AgentKey: "coder", ChatID: "chat", Definition: catalog.AgentDefinition{Key: "coder", Mode: "CODER", ContextTags: []string{"agents"}}})
	if err == nil || !strings.Contains(err.Error(), "workspace_unavailable") {
		t.Fatalf("required workspace failure was lost: %v", err)
	}
}
