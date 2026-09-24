package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"path/filepath"
	"testing"
)

func TestNativeConfiguredLifecycleDoesNotRequireClientOrCredentials(t *testing.T) {
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), BuiltinRoot: filepath.Join(root, "builtins"), StateRoot: filepath.Join(root, "state")}
	if err := connector.WriteBuiltin(filepath.Join(sources.BuiltinRoot, "builtin.desktop"), "desktop", "", "darwin"); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), sources, nil)
	initial, err := m.Connection(context.Background(), "builtin.desktop")
	if err != nil || initial.Configured {
		t.Fatalf("initial: %#v %v", initial, err)
	}
	if _, err := m.Check(context.Background(), "builtin.desktop", ""); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := m.Connection(context.Background(), "builtin.desktop")
	if unchanged.Configured {
		t.Fatal("check configured a new installation")
	}
	if _, err := m.Connect("builtin.desktop"); err != nil {
		t.Fatal(err)
	}
	configured, err := m.Connection(context.Background(), "builtin.desktop")
	if err != nil || !configured.Configured || configured.Readiness != "ready" {
		t.Fatalf("configured: %#v %v", configured, err)
	}
	if _, err := m.Disconnect(context.Background(), "builtin.desktop"); err != nil {
		t.Fatal(err)
	}
	final, _ := m.Connection(context.Background(), "builtin.desktop")
	if final.Configured {
		t.Fatal("disconnect failed")
	}
}
