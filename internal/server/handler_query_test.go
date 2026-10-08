package server

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func writeSkillRuntimeFixture(t *testing.T, root string, skillID string, env string) string {
	t.Helper()

	skillDir := filepath.Join(root, skillID)
	if err := os.MkdirAll(filepath.Join(skillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir skill hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+skillID+"\n\nskill"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if strings.TrimSpace(env) != "" {
		if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(env), 0o644); err != nil {
			t.Fatalf("write runtime env: %v", err)
		}
	}
	return skillDir
}

func TestResolveSkillRuntimeSettingsMergesEnvAndHookDirsInOrder(t *testing.T) {
	agentDir := t.TempDir()
	centerDir := t.TempDir()
	alphaDir := writeSkillRuntimeFixture(t, filepath.Join(agentDir, "skills"), "alpha", `{"NODE_ENV":"development","DEBUG":"1"}`)
	betaDir := writeSkillRuntimeFixture(t, filepath.Join(agentDir, "skills"), "beta", `{"NODE_ENV":"production","TZ":"UTC"}`)

	agentEnv := map[string]string{
		"NODE_ENV": "test",
		"BASE":     "1",
	}
	hookDirs, env, err := resolveSkillRuntimeSettings(agentEnv, agentDir, centerDir, []string{"alpha", "beta", "alpha"})
	if err != nil {
		t.Fatalf("resolveSkillRuntimeSettings() error = %v", err)
	}
	if !reflect.DeepEqual(hookDirs, []string{
		filepath.Join(alphaDir, ".bash-hooks"),
		filepath.Join(betaDir, ".bash-hooks"),
	}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	wantEnv := map[string]string{
		"NODE_ENV": "production",
		"BASE":     "1",
		"DEBUG":    "1",
		"TZ":       "UTC",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
	}
}

func TestResolveSkillRuntimeSettingsSkipsMissingSkills(t *testing.T) {
	centerDir := t.TempDir()
	runtimeDir := t.TempDir()
	betaDir := writeSkillRuntimeFixture(t, filepath.Join(runtimeDir, "skills"), "beta", `{"TZ":"UTC"}`)

	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}
	hookDirs, env, err := resolveSkillRuntimeSettings(agentEnv, runtimeDir, centerDir, []string{"missing", "beta"})
	if err != nil {
		t.Fatalf("resolveSkillRuntimeSettings() error = %v", err)
	}
	if !reflect.DeepEqual(hookDirs, []string{filepath.Join(betaDir, ".bash-hooks")}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	if !reflect.DeepEqual(env, map[string]string{"HTTP_PROXY": "http://agent", "TZ": "UTC"}) {
		t.Fatalf("env = %#v", env)
	}
}

func TestResolveSkillRuntimeSettingsSupportsHyphenatedSkillIDs(t *testing.T) {
	centerDir := t.TempDir()
	runtimeDir := t.TempDir()
	platformAdminDir := writeSkillRuntimeFixture(t, filepath.Join(runtimeDir, "skills"), "platform-admin", `{"DANGEROUS_COMMANDS":"1"}`)

	hookDirs, env, err := resolveSkillRuntimeSettings(nil, runtimeDir, centerDir, []string{"platform-admin"})
	if err != nil {
		t.Fatalf("resolveSkillRuntimeSettings() error = %v", err)
	}
	if !reflect.DeepEqual(hookDirs, []string{filepath.Join(platformAdminDir, ".bash-hooks")}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	if !reflect.DeepEqual(env, map[string]string{"DANGEROUS_COMMANDS": "1"}) {
		t.Fatalf("env = %#v", env)
	}
}

func TestResolveSkillRuntimeSettingsReturnsAgentEnvWithoutSkills(t *testing.T) {
	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}

	hookDirs, env, err := resolveSkillRuntimeSettings(agentEnv, "", "", nil)
	if err != nil {
		t.Fatalf("resolveSkillRuntimeSettings() error = %v", err)
	}
	if hookDirs != nil {
		t.Fatalf("hookDirs = %#v, want nil", hookDirs)
	}
	if !reflect.DeepEqual(env, agentEnv) {
		t.Fatalf("env = %#v, want %#v", env, agentEnv)
	}
	if env["HTTP_PROXY"] != "http://agent" {
		t.Fatalf("expected cloned env to preserve values, got %#v", env)
	}
}

type queryMemoryRegistry struct {
	testCatalogRegistry
	def catalog.AgentDefinition
}

func (r queryMemoryRegistry) DefaultAgentKey() string { return r.def.Key }

func (r queryMemoryRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if key == r.def.Key {
		return r.def, true
	}
	return catalog.AgentDefinition{}, false
}

func TestPrepareQueryPromotesUploadCreatedChatNameAndUpdatesAgentKey(t *testing.T) {
	chats, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	createdSummary, _, err := chats.EnsureChat("chat-agent-drift", "", "", "")
	if err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if createdSummary.ChatName != chat.PendingChatName {
		t.Fatalf("expected upload-created placeholder name, got %q", createdSummary.ChatName)
	}
	notifications := &recordingNotificationSink{}

	server := &Server{deps: Dependencies{
		Chats:         chats,
		Notifications: notifications,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-agent-drift","message":"use uploaded image"}`))
	prepared, err := prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}
	if prepared.Summary.AgentKey != "agent-a" {
		t.Fatalf("expected prepared summary agent-a, got %q", prepared.Summary.AgentKey)
	}
	if prepared.Summary.ChatName != "use uploaded image" {
		t.Fatalf("expected prepared chat name from first query, got %q", prepared.Summary.ChatName)
	}

	summary, err := chats.Summary("chat-agent-drift")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.AgentKey != "agent-a" {
		t.Fatalf("expected stored agent-a, got %q", summary.AgentKey)
	}
	if summary.ChatName != "use uploaded image" {
		t.Fatalf("expected stored chat name from first query, got %q", summary.ChatName)
	}
	if events := notifications.EventTypes(); !reflect.DeepEqual(events, []string{"chat.renamed"}) {
		t.Fatalf("expected chat.renamed notification, got %#v", events)
	}
	payloads := notifications.Payloads()
	if len(payloads) != 1 || payloads[0]["chatId"] != "chat-agent-drift" || payloads[0]["chatName"] != "use uploaded image" || payloads[0]["agentKey"] != "agent-a" {
		t.Fatalf("unexpected chat.renamed payload %#v", payloads)
	}
}

func TestPrepareQueryNonSandboxAgentCreatesChatDirectory(t *testing.T) {
	chatsRoot := t.TempDir()
	chats, err := chat.NewFileStoreAtStartup(chatsRoot)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			Paths:        config.PathsConfig{ChatsDir: chatsRoot},
			ContainerHub: config.ContainerHubConfig{Enabled: false},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-no-dir","message":"hello"}`))
	prepared, err := prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}
	if prepared.Session.AgentHasRuntimeSandbox {
		t.Fatal("expected non-sandbox session")
	}
	if stat, err := os.Stat(chats.ChatDir("chat-no-dir")); err != nil || !stat.IsDir() {
		t.Fatalf("expected chat directory to be created, stat=%#v err=%v", stat, err)
	}
	wantChatDir := absTestPath(t, chats.ChatDir("chat-no-dir"))
	if prepared.Session.RuntimeContext.LocalPaths.ChatDir != wantChatDir {
		t.Fatalf("chat dir = %q, want %q", prepared.Session.RuntimeContext.LocalPaths.ChatDir, wantChatDir)
	}
	if prepared.Session.RuntimeContext.LocalPaths.WorkspaceDir != "" {
		t.Fatalf("workspace dir = %q, want empty", prepared.Session.RuntimeContext.LocalPaths.WorkspaceDir)
	}
}

func TestPrepareQueryFailsFastWhenSandboxAgentRequiresDisabledContainerHub(t *testing.T) {
	chats, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			ContainerHub: config.ContainerHubConfig{Enabled: false},
			Paths:        config.PathsConfig{ChatsDir: t.TempDir()},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
				Runtime: map[string]any{
					"environmentId": "shell",
				},
				Workspace: catalog.AgentWorkspaceConfig{Root: t.TempDir()},
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"列出目录"}`))
	_, err = prepareQueryForTest(server, req)
	if err == nil {
		t.Fatal("expected prepareQueryForTest to fail when sandbox agent requires disabled container-hub")
	}
	if !strings.Contains(err.Error(), `agent "agent-a" requires sandbox but container-hub is disabled`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareQueryAllowsRuntimeEnvWithoutContainerHub(t *testing.T) {
	chats, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			ContainerHub: config.ContainerHubConfig{Enabled: false},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
				Runtime: map[string]any{
					"env": map[string]string{
						"HTTP_PROXY": "http://127.0.0.1:8001",
					},
				},
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"列出目录"}`))
	prepared, err := prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}
	if prepared.Session.AgentHasRuntimeSandbox {
		t.Fatal("expected env-only runtime config to avoid sandbox routing")
	}
	if got := prepared.Session.StaticRuntimeEnv["HTTP_PROXY"]; got != "http://127.0.0.1:8001" {
		t.Fatalf("StaticRuntimeEnv[HTTP_PROXY] = %q", got)
	}
	if containsString(prepared.Session.ToolNames, "bash") {
		t.Fatalf("runtime env must not grant bash, got %#v", prepared.Session.ToolNames)
	}
}

func TestPrepareQueryDesktopParamsDoNotGrantToolsOrRuntimeEnv(t *testing.T) {
	chats, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			ContainerHub: config.ContainerHubConfig{Enabled: false},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
				Runtime: map[string]any{
					"env": map[string]string{
						"CDP_HOST": "127.0.0.1",
						"CDP_PORT": "11789",
					},
				},
				Tools: []string{"datetime"},
			},
		},
	}}

	body := `{"agentKey":"agent-a","chatId":"chat-1","message":"看当前页面","params":{"desktop":{"surfaceId":"surface-a","source":"copilot"}}}`
	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(body))
	prepared, err := prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}

	if containsString(prepared.Session.ToolNames, "desktop_action") || containsString(prepared.Session.ToolNames, "surface_cdp") {
		t.Fatalf("did not expect desktop tools from params.desktop, got %#v", prepared.Session.ToolNames)
	}
	if !reflect.DeepEqual(prepared.Session.ToolNames, []string{"datetime"}) {
		t.Fatalf("unexpected tool names: %#v", prepared.Session.ToolNames)
	}
	expectedRuntimeEnv := map[string]string{
		"CDP_HOST": "127.0.0.1",
		"CDP_PORT": "11789",
	}
	if !reflect.DeepEqual(prepared.Session.StaticRuntimeEnv, expectedRuntimeEnv) {
		t.Fatalf("unexpected static runtime env: %#v", prepared.Session.StaticRuntimeEnv)
	}
}

func TestPrepareQueryCapsDesktopImageStudioZenmiRunToOneToolCall(t *testing.T) {
	chats, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	server := &Server{deps: Dependencies{
		Config: config.Config{ContainerHub: config.ContainerHubConfig{Enabled: false}},
		Chats:  chats,
		Registry: queryMemoryRegistry{def: catalog.AgentDefinition{
			Key: "zenmi", Name: "Zenmi", ModelKey: "mock-model", Tools: []string{"image_generate"},
		}},
	}}
	body := `{"agentKey":"zenmi","chatId":"chat-image-studio","message":"edit image","params":{"desktop":{"source":"copilot","action":"image_studio"}}}`
	prepared, err := prepareQueryForTest(server, httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(body)))
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}
	if prepared.Session.RunLimits.MaxToolCalls != 1 || prepared.Session.RunLimits.MaxToolRounds != 1 {
		t.Fatalf("unexpected Image Studio run limits: %#v", prepared.Session.RunLimits)
	}
	if strings.TrimSpace(prepared.Session.RunLimits.FinalAnswerPrompt) == "" {
		t.Fatal("expected a final-answer-only prompt after the single tool round")
	}

	normalBody := `{"agentKey":"zenmi","chatId":"chat-normal","message":"hello","params":{"desktop":{"source":"copilot","action":"chat"}}}`
	normal, err := prepareQueryForTest(server, httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(normalBody)))
	if err != nil {
		t.Fatalf("prepare normal query: %v", err)
	}
	if normal.Session.RunLimits.MaxToolCalls != 0 || normal.Session.RunLimits.MaxToolRounds != 0 {
		t.Fatalf("normal Zenmi run must keep its configured budget: %#v", normal.Session.RunLimits)
	}
}
