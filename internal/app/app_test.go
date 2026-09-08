package app

import (
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

func TestAppStartupIgnoresLegacyMCPRegistry(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"AP_RUNTIME_REGISTRIES_DIR", "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_KBASE_DIR", "AP_RUNTIME_PAN_DIR"} {
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
	for _, path := range []string{"connectors-center/demo/connector.json", "ru-connectors/demo/connector.json", "connector-state/.credentials/demo.json"} {
		if _, err := os.Stat(filepath.Join(root, "runtime", path)); err != nil {
			t.Fatalf("startup did not prepare %s: %v", path, err)
		}
	}
	if _, err := os.Stat(oldPackage); !os.IsNotExist(err) {
		t.Fatal("startup retained old package location")
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
