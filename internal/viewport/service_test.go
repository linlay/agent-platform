package viewport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform-runner-go/internal/engine"
)

func TestServiceLoadsLocalViewportAndFallbacks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.html"), []byte("<div>demo</div>"), 0o644); err != nil {
		t.Fatalf("write viewport file: %v", err)
	}

	service := NewService(NewRegistry(root), engine.NewNoopViewportClient())
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
