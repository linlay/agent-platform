package connectorauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

func TestTokenCLIEnvironmentSnapshotPairsAccountAndRevision(t *testing.T) {
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "center"), StateRoot: filepath.Join(root, "state")}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "cli", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "KEY", "label": "Key", "type": "password", "required": true}}}},
		"cli.json":       map[string]any{"versionCheck": map[string]any{"command": map[string]string{"darwin": "demo --version", "linux": "demo --version", "win32": "demo.exe --version"}, "minVersion": "1.0.0"}, "env": map[string]string{"SERVICE_KEY": "${KEY}"}},
	} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(t.Context(), sources, nil)
	if _, err := m.SetToken(context.Background(), "demo", map[string]string{"KEY": "fixture-0"}); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	var known sync.Map
	revision, _ := m.Revision("demo")
	known.Store(revision, "fixture-0")
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 20; i++ {
			value := fmt.Sprintf("fixture-%d", i)
			if _, err := m.SetToken(context.Background(), "demo", map[string]string{"KEY": value}); err != nil {
				done <- err
				return
			}
			revision, err := m.Revision("demo")
			if err != nil {
				done <- err
				return
			}
			known.Store(revision, value)
		}
		done <- nil
	}()
	type captured struct{ revision, value string }
	var snapshots []captured
	for i := 0; i < 80; i++ {
		env, revision, err := m.TokenCLIEnvironmentSnapshot(context.Background(), pkg, nil)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, captured{revision, hostenv.Value(env, "SERVICE_KEY")})
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshots {
		expected, ok := known.Load(s.revision)
		if !ok || expected != s.value {
			t.Fatal("captured account does not match its returned revision")
		}
	}
}
