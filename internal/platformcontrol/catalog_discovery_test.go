package platformcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func discoveryWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func discoveryQuery(t *testing.T, h *ToolHandler, action string, p map[string]any) map[string]any {
	t.Helper()
	result, err := h.Invoke(context.Background(), "catalog_query", map[string]any{"action": action, "args": p}, controlExecution())
	if err != nil || result.Error != "" {
		t.Fatalf("query failed: %+v %v", result, err)
	}
	return result.Structured
}

func TestProviderDiscoveryAndPagination(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 23; i++ {
		key := fmt.Sprintf("p%02d", i)
		discoveryWrite(t, filepath.Join(root, "providers", key+".yml"), "key: "+key+"\nbaseUrl: https://secret-host.invalid/token-secret\napiKey: credential-secret\n")
	}
	discoveryWrite(t, filepath.Join(root, "providers", "empty.yml"), "key: empty\napiKey: ''\n")
	for i := 0; i < 23; i++ {
		key := fmt.Sprintf("m%02d", i)
		discoveryWrite(t, filepath.Join(root, "models", key+".yml"), "key: "+key+"\nprovider: p00\nmodel: remote-model\n")
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(config.Config{}, nil, nil).ConfigureControl(nil, nil, registry)
	for _, typ := range []string{"provider", "model"} {
		first := discoveryQuery(t, h, "list", map[string]any{"resourceType": typ})
		if len(first["items"].([]any)) != 20 || first["hasMore"] != true {
			t.Fatalf("first page: %#v", first)
		}
		second := discoveryQuery(t, h, "list", map[string]any{"resourceType": typ, "cursor": first["nextCursor"], "limit": float64(100)})
		want := 3
		if typ == "provider" {
			want = 4
		}
		if len(second["items"].([]any)) != want || second["hasMore"] != false || second["nextCursor"] != "" {
			t.Fatalf("last page: %#v", second)
		}
		invalid := discoveryQuery(t, h, "list", map[string]any{"resourceType": typ, "status": "invalid"})
		if len(invalid["items"].([]any)) != 0 {
			t.Fatal(invalid)
		}
	}
	for _, key := range []string{"p00", "empty", "p22"} {
		v := discoveryQuery(t, h, "get", map[string]any{"resourceType": "provider", "resourceKey": key})
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "secret") || v["editable"] != false {
			t.Fatalf("unsafe provider: %s", b)
		}
		p := v["definition"].(map[string]any)
		if key == "p00" && (p["modelCount"] != float64(23) || p["credentialConfigured"] != true) {
			t.Fatal(p)
		}
		if key == "empty" && (p["modelCount"] != float64(0) || p["credentialConfigured"] != false) {
			t.Fatal(p)
		}
	}
	if _, err := h.readDiscoveryResource("provider", "missing"); err == nil {
		t.Fatal("missing provider accepted")
	}
	matrix := discoveryQuery(t, h, "resourceTypes", map[string]any{})
	if len(matrix["items"].([]any)) != 8 {
		t.Fatal(matrix)
	}
}

func TestMCPComponentsAreNotConnectors(t *testing.T) {
	root := t.TempDir()
	discoveryWrite(t, filepath.Join(root, "demo", "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"no_auth"}`)
	discoveryWrite(t, filepath.Join(root, "demo", "mcp.json"), `{"mcpServers":{"Main":{"url":"https://secret.invalid","headers":{"X-Key":"secret"}},"local":{"transport":"stdio","command":"secret-command","args":["secret"],"env":{"PASSWORD":"secret"},"enabled":false}}}`)
	h := NewToolHandler(config.Config{Paths: config.PathsConfig{ConnectorsCenterDir: root, NativePlatformControlDir: "../resources/connectors/builtin.platform-control", NativeWebControlDir: "../resources/connectors/builtin.web-control"}}, nil, nil)
	v := discoveryQuery(t, h, "list", map[string]any{"resourceType": "mcp"})
	if v["total"] != float64(2) {
		t.Fatal(v)
	}
	for _, key := range []string{"demo/Main", "demo/local"} {
		v := discoveryQuery(t, h, "get", map[string]any{"resourceType": "mcp", "resourceKey": key})
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "secret") || v["editable"] != false {
			t.Fatalf("unsafe mcp: %s", b)
		}
	}
	connectors := discoveryQuery(t, h, "list", map[string]any{"resourceType": "connector"})
	for _, raw := range connectors["items"].([]any) {
		item := raw.(map[string]any)
		if item["resourceKey"] == "demo" {
			if item["hasMcp"] != true || len(item["mcpKeys"].([]any)) != 2 {
				t.Fatal(item)
			}
		}
		if item["resourceKey"] == "builtin.platform-control" && item["hasMcp"] != false {
			t.Fatal(item)
		}
	}
	for _, key := range []string{"demo/missing", "../Main", "demo/../Main"} {
		if _, err := h.readDiscoveryResource("mcp", key); err == nil {
			t.Fatal(key)
		}
	}
	discoveryWrite(t, filepath.Join(root, "broken", "connector.json"), `{}`)
	if _, err := h.catalogQuery(context.Background(), "list", map[string]any{"resourceType": "mcp"}); err == nil {
		t.Fatal("incomplete enumeration reported success")
	}
}

func TestDiscoveryReadOnlyAndAdmission(t *testing.T) {
	cfg := config.Config{}
	sources := &adminsource.ControlService{Config: cfg}
	h := NewToolHandler(cfg, nil, nil).ConfigureControl(sources, nil, nil)
	for _, typ := range []string{"provider", "mcp", "model", "tool"} {
		for _, action := range []string{"apply", "delete"} {
			_, err := sources.Prepare(adminsource.ControlChange{ControlTarget: adminsource.ControlTarget{ResourceType: typ, ResourceKey: "demo"}, Action: action, Content: "key: demo", BaseRevision: "version"}, "caller")
			if err == nil {
				t.Fatalf("%s %s admitted mutation", typ, action)
			}
		}
		if err := sources.Validate(adminsource.ControlTarget{ResourceType: typ, ResourceKey: "demo"}, "key: demo"); err == nil {
			t.Fatalf("%s admitted validation", typ)
		}
	}
	for _, alter := range []func(*contracts.ExecutionContext){func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil }, func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" }} {
		e := controlExecution()
		alter(e)
		result, _ := h.Invoke(context.Background(), "catalog_query", map[string]any{"action": "resourceTypes"}, e)
		if result.Error == "" {
			t.Fatal("discovery bypassed caller admission")
		}
	}
	for _, typ := range []string{"provider", "mcp"} {
		if _, err := h.catalogQuery(context.Background(), "get", map[string]any{"resourceType": typ, "resourceKey": "demo", "path": "secret"}); err == nil {
			t.Fatal("read-only path accepted")
		}
	}
}
