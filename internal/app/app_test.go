package app

import (
	"agent-platform/internal/runtimeskills"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/config"
)

type blockingAutomation struct {
	done context.Context
}

func TestAppStartupIgnoresLegacyRuntimeSourcesAndState(t *testing.T) {
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
	legacy := filepath.Join(root, "runtime", "registries", "mcp-servers", "invalid.yml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("serverKey: [broken YAML deliberately ignored\n")
	if err := os.WriteFile(legacy, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "runtime.yml"), []byte("auth:\n  enabled: false\ncontainer-hub:\n  enabled: false\nautomation:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "tools.yml"), []byte("bash:\n  git-bash:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPackage := filepath.Join(root, "runtime", "connectors", "demo")
	if err := os.MkdirAll(oldPackage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPackage, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPackage, "cli.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	oldState := filepath.Join(root, "runtime", "connectors", ".credentials")
	if err := os.MkdirAll(oldState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldState, "demo.json"), []byte(`{"TOKEN":"test-state"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Conflicting and malformed legacy content must neither block startup nor
	// overwrite the current package or credentials.
	files := map[string]string{
		"ru-kbases/libraries/demo/index.sqlite":             "invalid legacy shared index",
		"ru-kbases/libraries/demo/state.json":               "invalid legacy shared state",
		"kbase/demo/index.sqlite":                           "invalid legacy Agent index",
		"kbase/demo/state.json":                             "invalid legacy Agent state",
		"connectors-center/demo/connector.json":             `{"id":"demo","name":"Current Demo","version":"2.0.0","type":"cli","auth_mode":"none"}`,
		"connectors-center/demo/cli.json":                   `{}`,
		".state/connectors/demo/credentials.json":           `{"TOKEN":"current-state"}`,
		"connectors/invalid/connector.json":                 "invalid legacy manifest",
		"connector-state/.credentials/demo.json":            "invalid legacy credentials",
		"connectors-center/.credentials/demo.json":          "ignored credentials",
		"connectors-center/.state/demo/data":                "ignored CLI state",
		"connectors-center/builtin.obsolete/connector.json": "ignored builtin copy",
		"connectors-center/.builtin-state/data":             "ignored builtin state",
	}
	for relative, value := range files {
		path := filepath.Join(root, "runtime", relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	agentDir := filepath.Join(root, "runtime", "agents", "demo")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: demo\nname: Demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application, err := New(ctx, config.LoadOptions{ConfigDir: root})
	if err != nil {
		t.Fatalf("legacy registry blocked startup: %v", err)
	}
	defer application.Close()
	recorder := httptest.NewRecorder()
	application.Router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health: %d %s", recorder.Code, recorder.Body.String())
	}
	for _, path := range []string{"connectors-center/demo/connector.json", ".state/connectors/demo/credentials.json"} {
		if _, err := os.Stat(filepath.Join(root, "runtime", path)); err != nil {
			t.Fatalf("startup did not prepare %s: %v", path, err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(root, "runtime", "ru-agents", "demo", "*", "connectors", "demo.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("versioned connector reference missing: %v %v", matches, err)
	}
	for relative, want := range files {
		if data, err := os.ReadFile(filepath.Join(root, "runtime", relative)); err != nil || string(data) != want {
			t.Fatalf("startup changed %s: %v", relative, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(oldState, "demo.json")); err != nil || string(data) != `{"TOKEN":"test-state"}` {
		t.Fatalf("startup changed old credentials: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldPackage, "connector.json")); err != nil {
		t.Fatalf("startup moved old package: %v", err)
	}
	if backups, err := filepath.Glob(filepath.Join(root, "runtime", ".connector-layout-backup-*")); err != nil || len(backups) != 0 {
		t.Fatalf("unexpected migration backups: %v %v", backups, err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil || string(data) != string(content) {
		t.Fatalf("startup changed ignored source: %v", err)
	}
}

func (s blockingAutomation) Stop() context.Context {
	return s.done
}

func TestAppCloseReturnsWhenAutomationStopTimesOut(t *testing.T) {
	previousTimeout := automationStopTimeout
	automationStopTimeout = 20 * time.Millisecond
	defer func() {
		automationStopTimeout = previousTimeout
	}()

	app := &App{
		automation: blockingAutomation{done: context.Background()},
	}

	startedAt := time.Now()
	if err := app.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}
	elapsed := time.Since(startedAt)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("expected close to return promptly after timeout, took %s", elapsed)
	}
}
