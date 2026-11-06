package app

import (
	"agent-platform/internal/processgroup"
	"agent-platform/internal/runtimeskills"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.WriteFile(filepath.Join(oldPackage, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"self"}`), 0644); err != nil {
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
		"connectors-center/demo/connector.json":             `{"id":"demo","name":"Current Demo","version":"2.0.0","type":"cli","authMode":"self"}`,
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

// Desktop's opt-in product smoke drives the real App through loopback HTTP.
// No fixture registry, direct snapshot import or alternate runtime assembly.
func TestDesktopACPProductServer(t *testing.T) {
	root := os.Getenv("ACP_PRODUCT_ROOT")
	bootstrap := os.Getenv("ACP_PRODUCT_BOOTSTRAP")
	if root == "" || bootstrap == "" {
		t.Skip("requires Desktop product smoke")
	}
	for _, key := range []string{"AP_RUNTIME_REGISTRIES_DIR", "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_PAN_DIR", "AP_RUNTIME_STATE_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("AP_RUNTIME_DIR", filepath.Join(root, "platform"))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	application, err := New(ctx, config.LoadOptions{ConfigDir: root, RuntimeMode: "desktop", AgentEngineBootstrapFile: bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	mux := http.NewServeMux()
	mux.Handle("/qa/", http.StripPrefix("/qa/", http.FileServer(http.Dir(filepath.Join(root, "ui")))))
	if webRoot := os.Getenv("ACP_PRODUCT_WEB_ROOT"); webRoot != "" {
		mux.Handle("/api/", application.Router)
		mux.Handle("/ws", application.Router)
		files := http.FileServer(http.Dir(webRoot))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/runtime-config.js":
				w.Header().Set("Content-Type", "application/javascript")
				w.Write([]byte(`globalThis.__AGENT_WEBCLIENT_RUNTIME_CONFIG__={BACKEND_MODE:"platform",DESKTOP_APP:"false",VOICE_ENABLED:"false"};`))
			case strings.HasPrefix(r.URL.Path, "/agent/"):
				http.ServeFile(w, r, filepath.Join(webRoot, "index.html"))
			default:
				files.ServeHTTP(w, r)
			}
		})
	} else {
		mux.Handle("/", application.Router)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(root, "platform.pid"), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "endpoint"), []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Desktop acceptance did not stop the server")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(root, "stop")); err == nil {
				return
			}
		}
	}
}

func TestMain(m *testing.M) {
	if handled, code := processgroup.RunGuardian(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestStandaloneAppPublishesReadyEngineManagement(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _ = runtimeskills.Remove(root) })
	for _, key := range []string{"AP_RUNTIME_REGISTRIES_DIR", "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_PAN_DIR", "AP_RUNTIME_STATE_DIR"} {
		t.Setenv(key, "")
	}
	runtimeRoot := filepath.Join(root, "runtime")
	t.Setenv("AP_RUNTIME_DIR", runtimeRoot)
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "runtime.yml"), []byte("auth:\n  enabled: false\ncontainer-hub:\n  enabled: false\nautomation:\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	application, err := New(context.Background(), config.LoadOptions{ConfigDir: root, RuntimeMode: "standalone"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if got := application.agentEngines.Directory(); !got.Ready || len(got.Engines) != 0 {
		t.Fatalf("standalone directory: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(runtimeRoot, "agent-engine-installations", "management.json")); err != nil {
		t.Fatal(err)
	}
}
