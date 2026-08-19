package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
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
		BaseURL:        server.URL,
		RequestTimeout: 1,
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
		BaseURL:        server.URL,
		RequestTimeout: 1,
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
		env, _ := payload["env"].(map[string]any)
		if env["AP_AGENT_CONFIG_HOME"] != "/agent/.config" || env["AP_WORKSPACE_DIR"] != "/workspace" || env["AP_CHAT_DIR"] != "/chat" {
			t.Fatalf("create session platform env = %#v", env)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cwd":"/workspace"}`))
	}))
	defer server.Close()

	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled:              true,
		BaseURL:              server.URL,
		DefaultEnvironmentID: "daily-office-pro",
		RequestTimeout:       1,
	}, paths)

	first := sandboxTestExecutionContext("run_shared", "req_alpha", sandboxWorkspace(paths))
	second := sandboxTestExecutionContext("run_shared", "req_beta", sandboxWorkspace(paths))
	if err := service.OpenIfNeeded(context.Background(), first); err != nil {
		t.Fatalf("first OpenIfNeeded() error = %v", err)
	}
	if err := service.OpenIfNeeded(context.Background(), second); err != nil {
		t.Fatalf("second OpenIfNeeded() error = %v", err)
	}

	if len(sessionIDs) != 1 {
		t.Fatalf("expected one create call reused by both contexts, got %#v", sessionIDs)
	}
	if !strings.HasPrefix(sessionIDs[0], "run-run_shared-") {
		t.Fatalf("unexpected create session ID: %#v", sessionIDs)
	}
	if first.SandboxSession.SessionID != sessionIDs[0] {
		t.Fatalf("unexpected first bound session ID: %#v", first.SandboxSession)
	}
	if second.SandboxSession.SessionID != sessionIDs[0] {
		t.Fatalf("unexpected second bound session ID: %#v", second.SandboxSession)
	}
	if _, err := os.Stat(filepath.Join(paths.ChatsDir, "chat_1")); err != nil {
		t.Fatalf("expected sandbox workspace chat directory to be created: %v", err)
	}
}

func TestLongLivedSandboxSessionsAreIsolatedByAgentAndChat(t *testing.T) {
	for _, level := range []string{"agent", "global"} {
		t.Run(level, func(t *testing.T) {
			var payloads []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sessions/create" {
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode create payload: %v", err)
				}
				payloads = append(payloads, payload)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"cwd":"/workspace"}`))
			}))
			defer server.Close()

			paths := sandboxTestPaths(t, "reader")
			service := NewContainerHubSandboxService(config.ContainerHubConfig{
				Enabled:              true,
				BaseURL:              server.URL,
				DefaultEnvironmentID: "daily-office-pro",
				RequestTimeout:       1,
			}, paths)

			first := sandboxTestExecutionContext("run-a", "req-a", sandboxWorkspace(paths))
			first.Session.RuntimeLevel = level
			first.Session.ChatID = "chat-a"
			second := sandboxTestExecutionContext("run-b", "req-b", sandboxWorkspace(paths))
			second.Session.RuntimeLevel = level
			second.Session.ChatID = "chat-b"
			if err := service.OpenIfNeeded(context.Background(), first); err != nil {
				t.Fatalf("first OpenIfNeeded() error = %v", err)
			}
			if err := service.OpenIfNeeded(context.Background(), second); err != nil {
				t.Fatalf("second OpenIfNeeded() error = %v", err)
			}

			if len(payloads) != 2 {
				t.Fatalf("cross-chat %s sessions must not be reused: %#v", level, payloads)
			}
			if first.SandboxSession.SessionID == second.SandboxSession.SessionID {
				t.Fatalf("cross-chat %s session IDs must differ: %q", level, first.SandboxSession.SessionID)
			}
			for index, payload := range payloads {
				wantChat := []string{"chat-a", "chat-b"}[index]
				wantWorkspace, err := filepath.EvalSymlinks(sandboxWorkspace(paths))
				if err != nil {
					t.Fatal(err)
				}
				wantChatRoot, err := filepath.EvalSymlinks(filepath.Join(paths.ChatsDir, wantChat))
				if err != nil {
					t.Fatal(err)
				}
				env, _ := payload["env"].(map[string]any)
				if env["AP_AGENT_CONFIG_HOME"] != "/agent/.config" || env["AP_WORKSPACE_DIR"] != "/workspace" || env["AP_CHAT_DIR"] != "/chat" {
					t.Fatalf("payload %d platform env = %#v", index, env)
				}
				if got := workspaceMountSource(payload); got != wantWorkspace {
					t.Fatalf("payload %d workspace source = %q, want %q", index, got, wantWorkspace)
				}
				if got := chatMountSource(payload); got != wantChatRoot {
					t.Fatalf("payload %d chat source = %q, want %q", index, got, wantChatRoot)
				}
			}
		})
	}
}

func workspaceMountSource(payload map[string]any) string {
	mounts, _ := payload["mounts"].([]any)
	for _, raw := range mounts {
		mount, _ := raw.(map[string]any)
		if mount["destination"] == "/workspace" {
			return strings.TrimSpace(contracts.AnyStringNode(mount["source"]))
		}
	}
	return ""
}

func chatMountSource(payload map[string]any) string {
	mounts, _ := payload["mounts"].([]any)
	for _, raw := range mounts {
		mount, _ := raw.(map[string]any)
		if mount["destination"] == "/chat" {
			return strings.TrimSpace(contracts.AnyStringNode(mount["source"]))
		}
	}
	return ""
}

func TestRunLevelSandboxSessionIDFallsBackToRunIDWithoutRequestID(t *testing.T) {
	got := runSessionID(contracts.QuerySession{RunID: "run_without_request"}, "mounts")
	if got != "run-run_without_request-mounts" {
		t.Fatalf("runSessionID() = %q, want %q", got, "run-run_without_request-mounts")
	}
}

func TestContainerHubCreateUsesDualRootV2AndMaskedPaths(t *testing.T) {
	var createPayload map[string]any
	runtimeInfoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runtime-info":
			runtimeInfoCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"engine":"docker","workspace_protocols":["dual-root-v2"]}`))
		case "/api/sessions/create":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"masked-session"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled: true, BaseURL: server.URL, DefaultEnvironmentID: "daily-office-pro", RequestTimeout: 1,
	}, paths)
	execCtx := sandboxTestExecutionContext("run-mask", "req-mask", filepath.Dir(paths.ChatsDir))
	execCtx.Session.ChatID = "chat-mask"
	if err := service.OpenIfNeeded(context.Background(), execCtx); err != nil {
		t.Fatalf("OpenIfNeeded() error = %v", err)
	}
	if runtimeInfoCalls != 1 {
		t.Fatalf("runtime info calls = %d, want 1", runtimeInfoCalls)
	}
	if createPayload["workspaceProtocol"] != workspaceChatSandboxProtocol {
		t.Fatalf("workspaceProtocol = %#v", createPayload["workspaceProtocol"])
	}
	masks, _ := createPayload["masked_paths"].([]any)
	if len(masks) != 1 || masks[0] != "/workspace/chats" {
		t.Fatalf("masked_paths = %#v", createPayload["masked_paths"])
	}
	if createPayload["cwd"] != "/workspace" {
		t.Fatalf("cwd = %#v", createPayload["cwd"])
	}
}

func TestContainerHubCreateRejectsMaskWhenHubDoesNotDeclareDualRootV2(t *testing.T) {
	createCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runtime-info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"engine":"docker"}`))
		case "/api/sessions/create":
			createCalled = true
		}
	}))
	defer server.Close()

	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled: true, BaseURL: server.URL, DefaultEnvironmentID: "daily-office-pro", RequestTimeout: 1,
	}, paths)
	execCtx := sandboxTestExecutionContext("run-old-hub", "req-old-hub", filepath.Dir(paths.ChatsDir))
	if err := service.OpenIfNeeded(context.Background(), execCtx); err == nil ||
		!strings.Contains(err.Error(), "does not support required workspace protocol") {
		t.Fatalf("OpenIfNeeded() error = %v", err)
	}
	if createCalled {
		t.Fatal("session create must not be sent to an incompatible Hub")
	}
}

func TestRunLevelSandboxSessionIDUsesSubTaskID(t *testing.T) {
	got := runSessionID(contracts.QuerySession{RunID: "run_1", SubTaskID: "sub_1"}, "mounts")
	if got != "run-run_1-sub_1-mounts" {
		t.Fatalf("runSessionID() = %q, want %q", got, "run-run_1-sub_1-mounts")
	}
}

func TestSandboxEnvironmentUsesReservedContainerContextAfterInvocationOverrides(t *testing.T) {
	env := mustSandboxCommandEnvironment(t, &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths:   contracts.LocalPaths{AgentDir: "/host/runtime/agents/reader"},
				SandboxPaths: contracts.SandboxPaths{AgentDir: "/agent", WorkspaceDir: "/workspace", ChatDir: "/chat"},
			},
		},
		StaticRuntimeEnv: map[string]string{
			"HTTP_PROXY":           "http://agent-proxy",
			"AP_AGENT_CONFIG_HOME": "/wrong-config",
			"AP_WORKSPACE_DIR":     "/wrong-workspace",
			"AP_CHAT_DIR":          "/wrong-chat",
		},
	}, map[string]string{
		"HTTP_PROXY":           "http://call-proxy",
		"AP_AGENT_CONFIG_HOME": "/call-config",
		"AP_WORKSPACE_DIR":     "/call-workspace",
		"AP_CHAT_DIR":          "/call-chat",
	})

	if got, want := env["AP_AGENT_CONFIG_HOME"], "/agent/.config"; got != want {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want %q", got, want)
	}
	if got, want := env["AP_WORKSPACE_DIR"], "/workspace"; got != want {
		t.Fatalf("AP_WORKSPACE_DIR = %q, want %q", got, want)
	}
	if got, want := env["AP_CHAT_DIR"], "/chat"; got != want {
		t.Fatalf("AP_CHAT_DIR = %q, want %q", got, want)
	}
	if got, want := env["HTTP_PROXY"], "http://call-proxy"; got != want {
		t.Fatalf("HTTP_PROXY = %q, want %q", got, want)
	}
	if strings.Contains(env["AP_AGENT_CONFIG_HOME"], "/host/") {
		t.Fatalf("sandbox config path leaked host path: %#v", env)
	}
	if _, ok := env["XDG_CONFIG_HOME"]; ok {
		t.Fatalf("sandbox environment must not synthesize XDG_CONFIG_HOME: %#v", env)
	}
	if _, ok := env["AP_SYSTEM_XDG_CONFIG_HOME"]; ok {
		t.Fatalf("sandbox environment must not synthesize AP_SYSTEM_XDG_CONFIG_HOME: %#v", env)
	}
}

func TestSandboxEnvironmentUsesLocalEnginePaths(t *testing.T) {
	env := mustSandboxCommandEnvironment(t, &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			RuntimeContext: contracts.RuntimeRequestContext{
				SandboxPaths: contracts.SandboxPaths{
					AgentDir:     "/runtime/agents/reader",
					WorkspaceDir: "/projects/reader",
					ChatDir:      "/runtime/chats/chat-a",
				},
			},
		},
	}, nil)

	if env["AP_AGENT_CONFIG_HOME"] != "/runtime/agents/reader/.config" {
		t.Fatalf("local AP_AGENT_CONFIG_HOME = %q", env["AP_AGENT_CONFIG_HOME"])
	}
	if env["AP_CHAT_DIR"] != "/runtime/chats/chat-a" {
		t.Fatalf("local AP_CHAT_DIR = %q", env["AP_CHAT_DIR"])
	}
	if env["AP_WORKSPACE_DIR"] != "/projects/reader" {
		t.Fatalf("local AP_WORKSPACE_DIR = %q", env["AP_WORKSPACE_DIR"])
	}
}

func TestSandboxSessionFingerprintChangesWithResolvedEnvironment(t *testing.T) {
	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled:              true,
		DefaultEnvironmentID: "daily-office-pro",
	}, paths)
	first := sandboxTestExecutionContext("run-a", "req-a", sandboxWorkspace(paths))
	second := sandboxTestExecutionContext("run-b", "req-b", sandboxWorkspace(paths))
	first.StaticRuntimeEnv = map[string]string{"HTTP_PROXY": "http://proxy-a"}
	second.StaticRuntimeEnv = map[string]string{"HTTP_PROXY": "http://proxy-b"}

	_, _, firstFingerprint, err := service.resolveSessionMountIdentity(first, "run")
	if err != nil {
		t.Fatal(err)
	}
	_, _, secondFingerprint, err := service.resolveSessionMountIdentity(second, "run")
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint == secondFingerprint {
		t.Fatalf("resolved environment change must produce a new session fingerprint: %q", firstFingerprint)
	}
}

func TestSandboxCommandSnapshotUpdatesWithoutChangingSessionFingerprint(t *testing.T) {
	paths := sandboxTestPaths(t, "reader")
	service := NewContainerHubSandboxService(config.ContainerHubConfig{Enabled: true, DefaultEnvironmentID: "daily-office-pro"}, paths)
	execCtx := sandboxTestExecutionContext("run-dynamic", "req-dynamic", sandboxWorkspace(paths))
	store := runenv.NewStore(filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "identity", "run-env.key"), runenv.Limits{})
	scope, err := store.NewScope(runenv.Identity{RunID: "run-dynamic", ChatID: "chat-1", Owner: "agent:reader", AgentKey: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Destroy()
	execCtx.StaticRuntimeEnv = map[string]string{"SESSION_CONTEXT": "static"}
	execCtx.RunEnvironment = scope
	_, _, beforeFingerprint, err := service.resolveSessionMountIdentity(execCtx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if value := sandboxSessionEnvironment(execCtx)["SESSION_CONTEXT"]; value != "static" {
		t.Fatalf("session base = %q", value)
	}
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "SESSION_CONTEXT", Value: "dynamic"}); err != nil {
		t.Fatal(err)
	}
	if value := mustSandboxCommandEnvironment(t, execCtx, nil)["SESSION_CONTEXT"]; value != "dynamic" {
		t.Fatalf("command snapshot = %q", value)
	}
	_, _, afterFingerprint, err := service.resolveSessionMountIdentity(execCtx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if beforeFingerprint != afterFingerprint {
		t.Fatalf("dynamic revision changed reuse fingerprint: %q != %q", beforeFingerprint, afterFingerprint)
	}
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationUnset, Name: "SESSION_CONTEXT"}); err != nil {
		t.Fatal(err)
	}
	if value := mustSandboxCommandEnvironment(t, execCtx, nil)["SESSION_CONTEXT"]; value != "static" {
		t.Fatalf("unset did not fall back to static: %q", value)
	}
	if err := scope.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxCommandEnvironment(execCtx, nil); !errors.Is(err, runenv.ErrClosed) {
		t.Fatalf("closed run environment snapshot error = %v, want ErrClosed", err)
	}
}

func TestLocalSandboxEngineUsesHostDualRootsAndWorkspaceCwd(t *testing.T) {
	workspace := t.TempDir()
	chatDir := t.TempDir()
	mounts := localEngineMounts([]MountSpec{
		{Name: "workspace", Source: workspace, Destination: "/workspace"},
		{Name: "chat-dir", Source: chatDir, Destination: "/chat"},
	})
	if mounts[0].Destination != workspace || mounts[1].Destination != chatDir {
		t.Fatalf("local engine mounts = %#v", mounts)
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			SandboxPaths: contracts.SandboxPaths{WorkspaceDir: workspace, ChatDir: chatDir},
		},
	}}
	if got := sandboxWorkspaceCwd(execCtx); got != workspace {
		t.Fatalf("local engine cwd = %q, want %q", got, workspace)
	}
}

func sandboxTestExecutionContext(runID string, requestID string, workspaceRoot string) *contracts.ExecutionContext {
	return sandboxTestExecutionContextWithSubTaskID(runID, requestID, "", workspaceRoot)
}

func mustSandboxCommandEnvironment(t *testing.T, execCtx *contracts.ExecutionContext, invocationEnv map[string]string) map[string]string {
	t.Helper()
	env, err := sandboxCommandEnvironment(execCtx, invocationEnv)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func sandboxTestExecutionContextWithSubTaskID(runID string, requestID string, subTaskID string, workspaceRoot string) *contracts.ExecutionContext {
	return &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			RequestID:              requestID,
			RunID:                  runID,
			SubTaskID:              subTaskID,
			ChatID:                 "chat_1",
			ChatRoot:               "",
			AgentKey:               "reader",
			WorkspaceRoot:          workspaceRoot,
			RuntimeEnvironmentID:   "daily-office-pro",
			RuntimeLevel:           "run",
			StaticRuntimeEnv:       map[string]string{},
			RuntimeExtraMounts:     nil,
			AgentHasRuntimeSandbox: true,
			RuntimeContext: contracts.RuntimeRequestContext{
				SandboxPaths: contracts.SandboxPaths{
					AgentDir:     "/agent",
					WorkspaceDir: "/workspace",
					ChatDir:      "/chat",
				},
			},
		},
	}
}

func sandboxWorkspace(paths config.PathsConfig) string {
	return filepath.Join(filepath.Dir(paths.ChatsDir), "workspace")
}

func sandboxTestPaths(t *testing.T, agentKey string) config.PathsConfig {
	t.Helper()
	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:    filepath.Join(root, "chats"),
		AgentsDir:   filepath.Join(root, "agents"),
		RUAgentsDir: filepath.Join(root, "ru-agents"),
		OwnerDir:    filepath.Join(root, "owner"),
		MemoryDir:   filepath.Join(root, "memory"),
	}
	if err := os.MkdirAll(filepath.Join(paths.RUAgentsDir, agentKey, "skills"), 0o755); err != nil {
		t.Fatalf("create test agent dir: %v", err)
	}
	if err := os.MkdirAll(sandboxWorkspace(paths), 0o755); err != nil {
		t.Fatalf("create test workspace: %v", err)
	}
	return paths
}
