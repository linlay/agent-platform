package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
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
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "credential-fixture")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf '%s' \"$AP_ACCESS_TOKEN\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(identity, []byte("synthetic-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{"fixture": root}
	ctx.Session.ConnectorBinDirs = []string{bin}
	ctx.Session.ConnectorCLIEntries = entries
	ctx.Session.ConnectorCredentials = []connector.CredentialEnvironment{{ID: "fixture", Mode: connector.AuthOneID}}
	executor := &RuntimeToolExecutor{cfg: config.Config{IdentityFile: identity, Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	t.Setenv("UNRELATED_SECRET", "never-inherit")
	for _, tc := range []struct{ command, want, code string }{
		{"credential-fixture", "synthetic-test-token", ""},
		{"printenv AP_ACCESS_TOKEN UNRELATED_SECRET", "", ""},
		{"credential-fixture; printenv AP_ACCESS_TOKEN", "", "connector_requires_direct_invocation"},
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
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: chatRoot}, AccessPolicy: config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelDefault: {Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "block"}}}}}
	executor := &RuntimeToolExecutor{cfg: cfg}
	ctx := &ExecutionContext{Session: QuerySession{ChatID: "chat-a", ChatRoot: chatDir, WorkspaceRoot: workspace, TempRoot: temp, TempRoots: []string{temp}, AccessLevel: AccessLevelDefault}}
	for _, input := range []map[string]any{{"file_path": external}, {"reference_name": "linked.png"}} {
		_, result, handled := executor.loadVisionImages(map[string]any{"images": []any{input}}, ctx, config.VisionRecognizeProfileConfig{})
		if !handled || result.Error != "vision_file_path_blocked" {
			t.Fatalf("image reference bypass: %+v", result)
		}
	}
}
