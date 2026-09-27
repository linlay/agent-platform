package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeNoAuthNeedsNeitherConfigurationNorClient(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), BuiltinRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	if err := connector.WriteBuiltin(filepath.Join(sources.BuiltinRoot, "builtin.desktop"), "desktop", ""); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), sources, nil)
	c, err := m.Connection(t.Context(), "builtin.desktop")
	if err != nil || c.ConfigurationRequired || c.Configured || c.Readiness != "no_auth" || c.Authentication.Status != "no_auth" || c.Capabilities.CanConnect || c.Capabilities.CanDisconnect || c.Capabilities.CanCheck {
		t.Fatalf("%#v %v", c, err)
	}
	for _, action := range []func() error{
		func() error { _, e := m.Connect("builtin.desktop"); return e },
		func() error { _, e := m.Disconnect(t.Context(), "builtin.desktop"); return e },
		func() error { _, e := m.Check(t.Context(), "builtin.desktop", ""); return e },
	} {
		if err := action(); err == nil || err.Error() != "connector_auth_not_required" {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(sources.StateRoot); !os.IsNotExist(err) {
		t.Fatalf("no_auth created state: %v", err)
	}
}
