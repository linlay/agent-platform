package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/runtimeskills"
)

func TestRouterRejectsUnmountedToolBeforeBackend(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{{Name: "regex"}}}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
	for _, names := range [][]string{nil, {}, {"datetime"}} {
		result, err := router.Invoke(context.Background(), "regex", map[string]any{}, &ExecutionContext{Session: QuerySession{ToolNames: names, ToolSetFrozen: true}})
		if err != nil || result.Error != "tool_not_mounted" || len(backend.calls) != 0 {
			t.Fatalf("unmounted tool executed: %+v %v", result, err)
		}
	}
}

func TestSearchExcludesProtectedStateAndOtherChats(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".state")
	chats := filepath.Join(root, "chats")
	for _, dir := range []string{state, filepath.Join(chats, "other")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"visible.txt", ".state/secret.txt", "chats/other/private.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("MATCH_SENTINEL"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{StateDir: state, ChatsDir: chats}}}
	ctx := bashExecutionContext(root)
	ctx.Session.ChatRoot = filepath.Join(chats, "current")
	if _, err := resolveRipgrepPath(); err != nil {
		t.Skip("rg unavailable")
	}
	for _, tool := range []string{"grep", "glob"} {
		args := map[string]any{"pattern": "**/*", "path": root}
		var result ToolExecutionResult
		var err error
		if tool == "grep" {
			args["pattern"], args["glob"] = "MATCH_SENTINEL", "**/*"
			result, err = executor.invokeGrep(context.Background(), args, ctx)
		} else {
			result, err = executor.invokeGlob(context.Background(), args, ctx)
		}
		if err != nil || result.Error != "" {
			t.Fatalf("%s: %+v %v", tool, result, err)
		}
		if strings.Contains(result.Output, "secret.txt") || strings.Contains(result.Output, "private.txt") || !strings.Contains(result.Output, "visible.txt") {
			t.Fatalf("%s leaked protected tree: %s", tool, result.Output)
		}
	}
}

func TestConnectorCredentialsReachOnlyVerifiedDirectChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execution fixture")
	}
	root := t.TempDir()
	paths := config.PathsConfig{
		AgentsDir: filepath.Join(root, "agents"), ConnectorsCenterDir: filepath.Join(root, "connectors"),
		RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills-center"),
	}
	pkgDir := filepath.Join(paths.ConnectorsCenterDir, "fixture")
	bin := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "credential-fixture")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf '%s' \"$AP_ID_TOKEN\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.AgentsDir, "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		filepath.Join(pkgDir, "connector.json"):             `{"id":"fixture","name":"Fixture","version":"1.0.0","type":"cli","authMode":"oneid"}`,
		filepath.Join(pkgDir, "cli.json"):                   `{}`,
		filepath.Join(paths.AgentsDir, "demo", "agent.yml"): "key: demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors: [fixture]\n",
	} {
		if err := os.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, dir := range []string{paths.RUAgentsDir, runtimeskills.Root(paths.RUAgentsDir)} {
			if err := runtimeskills.Remove(dir); err != nil {
				t.Error(err)
			}
		}
	})
	registry, err := catalog.NewFileRegistry(config.Config{Paths: paths}, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := registry.AgentDefinition("demo")
	if !ok || len(def.ConnectorCredentials) != 1 {
		t.Fatal("catalog did not retain oneid CLI credentials without templates")
	}
	identity := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(identity, []byte("synthetic-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{"fixture": def.ConnectorMounts[0].Dir}
	ctx.Session.ConnectorBinDirs = def.ConnectorBinDirs
	ctx.Session.ConnectorCLIEntries = def.ConnectorCLIEntries
	ctx.Session.ConnectorCredentials = def.ConnectorCredentials
	executor := &RuntimeToolExecutor{cfg: config.Config{SsoIdTokenFile: identity, Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	t.Setenv("UNRELATED_SECRET", "never-inherit")
	for _, tc := range []struct{ command, want, code string }{
		{"credential-fixture", "synthetic-test-token", ""},
		{"printenv AP_ID_TOKEN UNRELATED_SECRET", "", ""},
		{"credential-fixture; printenv AP_ID_TOKEN", "", "connector_requires_direct_invocation"},
	} {
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": tc.command}, ctx)
		if err != nil || result.Error != tc.code {
			t.Fatalf("%s: %+v %v", tc.command, result, err)
		}
		if tc.want != "" && !strings.Contains(result.Output, tc.want) {
			t.Fatalf("connector missing credential: %+v", result)
		}
		if tc.want == "" && (strings.Contains(result.Output, "synthetic-test-token") || strings.Contains(result.Output, "never-inherit")) {
			t.Fatalf("credential escaped: %+v", result)
		}
	}
	ctx.Session.ConnectorCredentials[0].Env = map[string]string{"SERVICE_TOKEN": "${AP_ID_TOKEN}"}
	for _, token := range []string{"rotated-identity", ""} {
		if err := os.WriteFile(identity, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		env, err := executor.connectorCommandEnvironment(context.Background(), ctx, "fixture", nil)
		if token == "" {
			if err == nil {
				t.Fatal("empty identity accepted")
			}
			continue
		}
		if err != nil || !containsString(env, "AP_ID_TOKEN="+token) || !containsString(env, "SERVICE_TOKEN="+token) {
			t.Fatal("direct child credentials do not match current identity", err)
		}
	}
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	for _, templates := range []map[string]string{nil, {"SERVICE_TOKEN": "${AP_ID_TOKEN}"}} {
		ctx.Session.ConnectorCredentials[0].Env = templates
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "credential-fixture"}, ctx)
		if err != nil || result.Error != "connector_credentials_unavailable" || result.ExitCode != -1 {
			t.Fatal("oneid child started without identity", err)
		}
	}
}

func TestImageReferencesUseCanonicalAccessPolicy(t *testing.T) {
	root := t.TempDir()
	chatRoot := filepath.Join(root, "chats")
	chatDir := filepath.Join(chatRoot, "chat-a")
	workspace := filepath.Join(root, "workspace")
	temp := filepath.Join(root, "temp")
	external := filepath.Join(root, "outside.png")
	for _, p := range []string{chatDir, workspace, temp} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(external, []byte("\x89PNG\r\n\x1a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(chatDir, "linked.png")); err != nil {
		t.Skip(err)
	}
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: chatRoot}, AccessPolicy: config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelAskForApproval: {Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "block"}}}}}
	executor := &RuntimeToolExecutor{cfg: cfg}
	ctx := &ExecutionContext{Session: QuerySession{ChatID: "chat-a", ChatRoot: chatDir, WorkspaceRoot: workspace, TempRoot: temp, TempRoots: []string{temp}, AccessLevel: AccessLevelAskForApproval}}
	for _, input := range []map[string]any{{"filePath": external}, {"referenceName": "linked.png"}} {
		_, result, handled := executor.loadVisionImages(map[string]any{"images": []any{input}}, ctx, config.VisionRecognizeProfileConfig{})
		if !handled || result.Error != "vision_file_path_blocked" {
			t.Fatalf("image reference bypass: %+v", result)
		}
	}
}
