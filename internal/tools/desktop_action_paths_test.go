package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

func TestDesktopActionAliasesMatchFileTools(t *testing.T) {
	root := t.TempDir()
	chat := filepath.Join(root, "chats", "current")
	if err := os.MkdirAll(chat, 0700); err != nil {
		t.Fatal(err)
	}
	session := QuerySession{WorkspaceRoot: root, RuntimeContext: RuntimeRequestContext{LocalPaths: LocalPaths{ChatDir: chat}}}
	for _, input := range []string{"@chat/webapps/中文 工作台/distribution", "@workspace/webapps/demo/distribution", `@chat\webapps\demo\distribution`} {
		relative, err := resolveDesktopActionAlias(session, input)
		if err != nil {
			t.Fatal(err)
		}
		fromFiles, err := accesspolicy.ResolveSessionPath(session, strings.ReplaceAll(input, "\\", "/"))
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := pathutil.Canonicalize(fromFiles)
		actual, _ := pathutil.Canonicalize(filepath.Join(root, filepath.FromSlash(relative)))
		if actual.Key != expected.Key {
			t.Fatalf("paths differ: %q != %q", actual.Host, expected.Host)
		}
	}
	// A system/volume root Workspace is valid; the destination is a writable descendant.
	if runtime.GOOS == "windows" {
		session.WorkspaceRoot = filepath.VolumeName(root) + string(filepath.Separator)
	} else {
		session.WorkspaceRoot = "/"
	}
	if _, err := resolveDesktopActionAlias(session, "@chat/webapps/demo/distribution"); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopActionAliasFieldsAndImmutableInputs(t *testing.T) {
	root := t.TempDir()
	session := QuerySession{WorkspaceRoot: root, ChatRoot: filepath.Join(root, "chat")}
	for action, fields := range desktopActionPathFields {
		for _, field := range fields {
			args := map[string]any{field: "@chat/webapps/demo", "label": "@chat/leave-me", "patch": map[string]any{"path": "@chat/leave-me"}}
			got, err := resolveDesktopActionPaths(session, action, args)
			if err != nil {
				t.Fatal(err)
			}
			expected := "chat/webapps/demo"
			if action == "desktop.webapp.install" {
				canonical, _ := pathutil.Canonicalize(filepath.Join(root, "chat", "webapps", "demo"))
				expected = canonical.Host
			}
			if got[field] != expected || args[field] != "@chat/webapps/demo" || got["label"] != args["label"] {
				t.Fatalf("bad rewrite: %#v %#v", got, args)
			}
		}
	}
	got, err := resolveDesktopActionPaths(session, "desktop.theme.get", map[string]any{"projectPath": "@chat/no-rewrite"})
	if err != nil || got["projectPath"] != "@chat/no-rewrite" {
		t.Fatal(got, err)
	}
	got, err = resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"archivePath": "releases/generated.zip"})
	if err != nil || got["archivePath"] != "releases/generated.zip" {
		t.Fatal(got, err)
	}
}

func TestDesktopInstallAliasesAllowChatOutsideWorkspace(t *testing.T) {
	workspace, chat := t.TempDir(), t.TempDir()
	workspace, _ = filepath.EvalSymlinks(workspace)
	chat, _ = filepath.EvalSymlinks(chat)
	session := QuerySession{WorkspaceRoot: workspace, ChatRoot: chat}
	for _, tc := range []struct{ input, expected string }{
		{"@chat/workbench.zip", filepath.Join(chat, "workbench.zip")},
		{`@chat\中文 工作台.zip`, filepath.Join(chat, "中文 工作台.zip")},
		{"@workspace/release/workbench.zip", filepath.Join(workspace, "release", "workbench.zip")},
	} {
		got, err := resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"archivePath": tc.input})
		if err != nil || got["archivePath"] != tc.expected {
			t.Fatalf("install alias: %#v %v", got, err)
		}
	}
	if _, err := resolveDesktopActionAlias(session, "@chat/workbench.zip"); err == nil {
		t.Fatal("Tooling accepted a Chat path outside Workspace")
	}
	session.WorkspaceRoot = ""
	got, err := resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"archivePath": "@chat/workbench.zip"})
	if err != nil || got["archivePath"] != filepath.Join(chat, "workbench.zip") {
		t.Fatalf("Chat install requires no Workspace: %#v %v", got, err)
	}
	for _, input := range []string{"@workspace/workbench.zip", "@chat/../outside.zip", "@chat//absolute.zip", "@chat/C:/app.zip", "@agent/app.zip"} {
		if _, err := resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"archivePath": input}); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(chat, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			t.Log("symlink test unavailable:", err)
			return
		}
		t.Fatal(err)
	}
	if _, err := resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"archivePath": "@chat/escape/app.zip"}); err == nil {
		t.Fatal("install alias followed a link outside the Chat root")
	}
}

func TestDesktopInstallHostPathsRemainUnchanged(t *testing.T) {
	for _, input := range []string{"/Users/example/Downloads/workbench.zip", `C:\Users\example\Downloads\workbench.zip`, "release/workbench.zip"} {
		got, err := resolveDesktopActionPaths(QuerySession{}, "desktop.webapp.install", map[string]any{"archivePath": input})
		if err != nil || got["archivePath"] != input {
			t.Fatalf("host path changed: %#v %v", got, err)
		}
	}
}

func TestDesktopInstallChatAliasDispatchWithoutWorkspace(t *testing.T) {
	chat := t.TempDir()
	code := 0
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{Frame: "response", Type: "desktop.webapp.install", Code: &code, Data: []byte(`{"ok":true,"action":"desktop.webapp.install","result":{"webappId":"webapp-0123456789abcdef","operation":"installed"}}`)}}}
	executor := (&RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}}).
		WithClientRequestInvoker(invoker).
		WithDesktopMainTargetProvider(&desktopMainTargetProviderStub{target: ClientTarget{SessionID: "desktop"}, state: DesktopMainTargetReady})
	execCtx := desktopActionTestExecutionContext()
	execCtx.Session.WebClientTarget = ClientTarget{SessionID: "desktop"}
	execCtx.Session.WorkspaceRoot = ""
	execCtx.Session.ChatRoot = chat
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{"action": "desktop.webapp.install", "args": map[string]any{"archivePath": "@chat/workbench.zip"}}, execCtx)
	if err != nil || result.ExitCode != 0 || invoker.calls != 1 {
		t.Fatalf("not dispatched: %#v %v", result, err)
	}
	expected, _ := pathutil.Canonicalize(filepath.Join(chat, "workbench.zip"))
	if invoker.request.Payload["archivePath"] != expected.Host || invoker.request.Source.WorkspaceRoot != "" {
		t.Fatalf("alias or source changed: %#v", invoker.request)
	}
	rejected, err := executor.invokeDesktopAction(context.Background(), map[string]any{"action": "desktop.webapp.install", "args": map[string]any{"archivePath": "@chat/../outside.zip"}}, execCtx)
	if err != nil || rejected.Error != "invalid_args" || invoker.calls != 1 {
		t.Fatalf("invalid alias dispatched: %#v %v", rejected, err)
	}
	if !strings.Contains(rejected.Output, "does not require a project Workspace") {
		t.Fatal("install recovery still requires Workspace")
	}
}

func TestDesktopActionAliasesRejectEscapes(t *testing.T) {
	root := t.TempDir()
	chat := filepath.Join(root, "chat")
	if err := os.MkdirAll(chat, 0700); err != nil {
		t.Fatal(err)
	}
	session := QuerySession{WorkspaceRoot: root, ChatRoot: chat}
	for _, input := range []string{"@agent/a", "@chat-other/a", "@chat/../a", `@chat\..\a`, "@chat//absolute", "@chat/C:/a", "@chat/file\x00"} {
		if _, err := resolveDesktopActionAlias(session, input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, broken := range []QuerySession{{ChatRoot: chat}, {WorkspaceRoot: root}, {WorkspaceRoot: chat, ChatRoot: t.TempDir()}} {
		if _, err := resolveDesktopActionAlias(broken, "@chat/a"); err == nil {
			t.Errorf("accepted unavailable/outside root: %#v", broken)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(chat, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			t.Log("symlink test unavailable:", err)
			return
		}
		t.Fatal(err)
	}
	if _, err := resolveDesktopActionAlias(session, "@chat/escape/new/file"); err == nil {
		t.Fatal("accepted symlink escape")
	}
}

func TestDesktopActionAliasDispatch(t *testing.T) {
	root := t.TempDir()
	code := 0
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{Frame: "response", Type: "desktop.webapp.package.init", Code: &code, Data: []byte(`{"ok":true,"action":"desktop.webapp.package.init","result":{"id":"webapp-0123456789abcdef"}}`)}}}
	executor := (&RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}}).
		WithClientRequestInvoker(invoker).
		WithDesktopMainTargetProvider(&desktopMainTargetProviderStub{target: ClientTarget{SessionID: "desktop"}, state: DesktopMainTargetReady})
	execCtx := desktopActionTestExecutionContext()
	execCtx.Session.WebClientTarget = ClientTarget{SessionID: "desktop"}
	execCtx.Session.WorkspaceRoot = root
	execCtx.Session.ChatRoot = filepath.Join(root, "chat")
	first, err := executor.invokeDesktopAction(context.Background(), map[string]any{"action": "desktop.webapp.package.init", "args": map[string]any{"projectPath": "@chat/webapps/demo/distribution", "key": "demo", "label": "Demo"}}, execCtx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExitCode != 0 || invoker.calls != 1 {
		t.Fatalf("not dispatched: %#v", invoker)
	}
	if invoker.request.Payload["projectPath"] != "chat/webapps/demo/distribution" {
		t.Fatalf("alias leaked: %#v", invoker.request)
	}
	if invoker.request.Source == nil || invoker.request.Source.WorkspaceRoot != root {
		t.Fatalf("source changed: %#v", invoker.request.Source)
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{"action": "desktop.webapp.package.init", "args": map[string]any{"projectPath": "@chat/../other"}}, execCtx)
	if err != nil || result.Error != "invalid_args" || invoker.calls != 1 {
		t.Fatalf("invalid path dispatched: %#v %v", result, err)
	}
}
