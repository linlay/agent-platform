package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/runtimeskills"
)

func TestAppStartsWithInvalidConnectors(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		if err := runtimeskills.Remove(root); err != nil {
			t.Error(err)
		}
	})
	for _, key := range []string{"AP_RUNTIME_REGISTRIES_DIR", "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_PAN_DIR", "AP_RUNTIME_STATE_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("AP_RUNTIME_DIR", filepath.Join(root, "runtime"))
	files := map[string]string{
		"configs/runtime.yml": "auth:\n  enabled: false\ncontainer-hub:\n  enabled: false\nautomation:\n  enabled: false\n",
		"configs/tools.yml":   "bash:\n  git-bash:\n    enabled: false\n",
		"runtime/connectors-center/healthy/connector.json":        `{"id":"healthy","name":"Healthy","version":"1.0.0","type":"cli","authMode":"none"}`,
		"runtime/connectors-center/healthy/cli.json":              `{}`,
		"runtime/connectors-center/tencent-map/connector.json":    `{"id":"tencent-map","name":"Map","version":"1.0.0","type":"mcp","authMode":"token"}`,
		"runtime/connectors-center/tencent-map/token-schema.json": `{"title_en":"Map","fields":[{"key":"TOKEN","required":true}]}`,
		"runtime/connectors-center/tencent-map/mcp.json":          `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`,
		"runtime/connectors-center/bad-mcp/connector.json":        `{"id":"bad-mcp","name":"Bad MCP","version":"1.0.0","type":"mcp","authMode":"none"}`,
		"runtime/connectors-center/bad-mcp/mcp.json":              `{"mcpServers":{"main":{"type":"streamableHttp","url":"invalid-url"}}}`,
		"runtime/agents/healthy/agent.yml":                        "key: healthy\nname: Healthy\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors: [healthy]\n",
		"runtime/agents/broken/agent.yml":                         "key: broken\nname: Broken\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors: [tencent-map]\n",
		"runtime/agents/mcp-agent/agent.yml":                      "key: mcp-agent\nname: MCP Agent\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors: [bad-mcp]\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application, err := New(ctx, config.LoadOptions{ConfigDir: root})
	if err != nil {
		t.Fatalf("one invalid connector blocked app startup: %v", err)
	}
	defer application.Close()
	for _, path := range []string{"/healthz", "/api/connectors", "/api/agents"} {
		recorder := httptest.NewRecorder()
		application.Router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s unavailable: %d %s", path, recorder.Code, recorder.Body.String())
		}
		if path == "/api/agents" {
			var response api.ApiResponse[[]api.AgentSummary]
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, agent := range response.Data {
				if agent.Key == "broken" {
					t.Fatal("Agent with invalid package must stay isolated")
				}
				found = found || agent.Key == "healthy"
			}
			if !found {
				t.Fatal("healthy Agent unavailable")
			}
		}
	}
}
