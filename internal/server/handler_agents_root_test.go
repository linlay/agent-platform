package server

import (
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
	"encoding/json"
	gws "github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootAgentWorkspaceOmittedOverHTTPAndWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		setupRuntime: func(_ string, cfg *config.Config) {
			file := filepath.Join(cfg.Paths.AgentsDir, "root-agent.yml")
			if err := os.WriteFile(file, []byte("key: root-agent\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: \"@root\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	})
	check := func(items []map[string]any) {
		t.Helper()
		for _, item := range items {
			if item["key"] == "root-agent" {
				if _, ok := item["workspaceDir"]; ok {
					t.Fatalf("root agent exposed as project: %#v", item)
				}
				return
			}
		}
		t.Fatal("root agent missing from catalog")
	}
	for _, endpoint := range []string{"/api/agents?scope=nav", "/api/agents?scope=nav&includeTeam=true", "/api/admin/agents"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", endpoint, rec.Code, rec.Body.String())
		}
		var response struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		check(response.Data)
	}
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, includeTeam := range []bool{false, true} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/agents", ID: "root-agents", Payload: marshalPayload(map[string]any{"scope": "nav", "includeTeam": includeTeam})}); err != nil {
			t.Fatal(err)
		}
		var frame ws.ResponseFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Code != 0 {
			t.Fatalf("response: %#v", frame)
		}
		items, err := marshalResponseData[[]map[string]any](frame.Data)
		if err != nil {
			t.Fatal(err)
		}
		check(items)
	}
}
