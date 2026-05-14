package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

func TestCreateSessionIncludesContainerHubErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/create" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"validation failed: mount source does not exist: /missing-pan"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	_, err := client.CreateSession(context.Background(), map[string]any{"session_id": "run-test"})
	if err == nil {
		t.Fatal("CreateSession() expected error")
	}
	if !strings.Contains(err.Error(), "/api/sessions/create returned status 400: validation failed: mount source does not exist: /missing-pan") {
		t.Fatalf("CreateSession() error = %q", err.Error())
	}
}

func TestExecuteSessionIncludesContainerHubErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"session is stopped; recreate it before executing commands"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	_, _, err := client.ExecuteSessionRaw(context.Background(), "run-test", map[string]any{"command": "/bin/sh"})
	if err == nil {
		t.Fatal("ExecuteSessionRaw() expected error")
	}
	if !strings.Contains(err.Error(), "/api/sessions/execute returned status 409: session is stopped; recreate it before executing commands") {
		t.Fatalf("ExecuteSessionRaw() error = %q", err.Error())
	}
}

func TestRunLevelSandboxSessionIDReusesRunIDAcrossRequestIDs(t *testing.T) {
	var sessionIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/create" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode create payload: %v", err)
		}
		sessionID := strings.TrimSpace(contracts.AnyStringNode(payload["session_id"]))
		sessionIDs = append(sessionIDs, sessionID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cwd":"/workspace/chat_1"}`))
	}))
	defer server.Close()

	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled:              true,
		BaseURL:              server.URL,
		DefaultEnvironmentID: "daily-office-pro",
		RequestTimeoutMs:     1000,
	}, paths)

	first := sandboxTestExecutionContext("run_shared", "req_alpha")
	second := sandboxTestExecutionContext("run_shared", "req_beta")
	if err := service.OpenIfNeeded(context.Background(), first); err != nil {
		t.Fatalf("first OpenIfNeeded() error = %v", err)
	}
	if err := service.OpenIfNeeded(context.Background(), second); err != nil {
		t.Fatalf("second OpenIfNeeded() error = %v", err)
	}

	if len(sessionIDs) != 1 {
		t.Fatalf("expected one create call reused by both contexts, got %#v", sessionIDs)
	}
	if sessionIDs[0] != "run-run_shared" {
		t.Fatalf("unexpected create session ID: %#v", sessionIDs)
	}
	if first.SandboxSession.SessionID != "run-run_shared" {
		t.Fatalf("unexpected first bound session ID: %#v", first.SandboxSession)
	}
	if second.SandboxSession.SessionID != "run-run_shared" {
		t.Fatalf("unexpected second bound session ID: %#v", second.SandboxSession)
	}
}

func TestRunLevelSandboxSessionIDFallsBackToRunIDWithoutRequestID(t *testing.T) {
	got := runSessionID(contracts.QuerySession{RunID: "run_legacy"})
	if got != "run-run_legacy" {
		t.Fatalf("runSessionID() = %q, want %q", got, "run-run_legacy")
	}
}

func TestRunLevelSandboxSessionIDUsesSubTaskID(t *testing.T) {
	got := runSessionID(contracts.QuerySession{RunID: "run_1", SubTaskID: "sub_1"})
	if got != "run-run_1-sub_1" {
		t.Fatalf("runSessionID() = %q, want %q", got, "run-run_1-sub_1")
	}
}

func sandboxTestExecutionContext(runID string, requestID string) *contracts.ExecutionContext {
	return sandboxTestExecutionContextWithSubTaskID(runID, requestID, "")
}

func sandboxTestExecutionContextWithSubTaskID(runID string, requestID string, subTaskID string) *contracts.ExecutionContext {
	return &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			RequestID:              requestID,
			RunID:                  runID,
			SubTaskID:              subTaskID,
			ChatID:                 "chat_1",
			AgentKey:               "reader",
			RuntimeEnvironmentID:   "daily-office-pro",
			RuntimeLevel:           "run",
			RuntimeEnvOverrides:    map[string]string{},
			RuntimeExtraMounts:     nil,
			AgentHasRuntimeSandbox: true,
		},
	}
}

func sandboxTestPaths(t *testing.T, agentKey string) config.PathsConfig {
	t.Helper()
	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:  filepath.Join(root, "chats"),
		AgentsDir: filepath.Join(root, "agents"),
		OwnerDir:  filepath.Join(root, "owner"),
		MemoryDir: filepath.Join(root, "memory"),
	}
	if err := os.MkdirAll(filepath.Join(paths.AgentsDir, agentKey), 0o755); err != nil {
		t.Fatalf("create test agent dir: %v", err)
	}
	return paths
}
