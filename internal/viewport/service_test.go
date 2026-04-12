package viewport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform-runner-go/internal/contracts"
)

func TestServiceLoadsLocalViewportAndFallbacks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.html"), []byte("<div>demo</div>"), 0o644); err != nil {
		t.Fatalf("write viewport file: %v", err)
	}

	service := NewService(NewRegistry(root), contracts.NewNoopViewportClient())
	local, err := service.Get(context.Background(), "demo")
	if err != nil {
		t.Fatalf("load local viewport: %v", err)
	}
	if local["html"] != "<div>demo</div>" {
		t.Fatalf("unexpected local viewport payload: %#v", local)
	}

	fallback, err := service.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("fallback viewport: %v", err)
	}
	if fallback["status"] != "not_implemented" {
		t.Fatalf("expected fallback payload, got %#v", fallback)
	}
}

func TestRegistryProvidesDefaultConfirmDialog(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	payload, ok, err := registry.Get("confirm_dialog")
	if err != nil {
		t.Fatalf("get confirm dialog: %v", err)
	}
	if !ok {
		t.Fatal("expected confirm dialog to exist")
	}
	if payload["viewportKey"] != "confirm_dialog" {
		t.Fatalf("unexpected payload %#v", payload)
	}
}

func TestServerRegistryLoadsViewportServersDirectoryAndServerKey(t *testing.T) {
	registriesRoot := t.TempDir()
	serversRoot := DefaultServersRoot(registriesRoot)
	if err := os.MkdirAll(serversRoot, 0o755); err != nil {
		t.Fatalf("create viewport servers dir: %v", err)
	}
	config := "serverKey: weather\nbaseUrl: http://127.0.0.1:11969\nendpointPath: /mcp\n"
	if err := os.WriteFile(filepath.Join(serversRoot, "mock.yml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write viewport server config: %v", err)
	}

	servers, err := NewServerRegistry(serversRoot).List()
	if err != nil {
		t.Fatalf("list viewport servers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Key != "weather" {
		t.Fatalf("expected server key from serverKey field, got %#v", servers[0])
	}
}

func TestServiceLoadsRemoteHTMLViewportBeforeFallback(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "viewports/get" {
			t.Fatalf("unexpected method %q", req.Method)
		}
		if req.Params["viewportKey"] != "show_weather_card" {
			t.Fatalf("unexpected params %#v", req.Params)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"viewportKey":  "show_weather_card",
				"viewportType": "html",
				"payload":      "<html><body>weather</body></html>",
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer remote.Close()

	registriesRoot := t.TempDir()
	if err := os.MkdirAll(DefaultRoot(registriesRoot), 0o755); err != nil {
		t.Fatalf("create viewports dir: %v", err)
	}
	serversRoot := DefaultServersRoot(registriesRoot)
	if err := os.MkdirAll(serversRoot, 0o755); err != nil {
		t.Fatalf("create viewport servers dir: %v", err)
	}
	config := "serverKey: mock\nbaseUrl: " + remote.URL + "\nendpointPath: /mcp\n"
	if err := os.WriteFile(filepath.Join(serversRoot, "mock.yml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write viewport server config: %v", err)
	}

	service := NewServiceWithServers(
		NewRegistry(DefaultRoot(registriesRoot)),
		NewSyncer(NewServerRegistry(serversRoot), remote.Client()),
		contracts.NewNoopViewportClient(),
	)

	payload, err := service.Get(context.Background(), "show_weather_card")
	if err != nil {
		t.Fatalf("load remote viewport: %v", err)
	}
	if payload["html"] != "<html><body>weather</body></html>" {
		t.Fatalf("expected remote html payload, got %#v", payload)
	}
	if _, exists := payload["status"]; exists {
		t.Fatalf("expected remote payload before fallback, got %#v", payload)
	}
}
