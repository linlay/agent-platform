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
			if got[field] != "chat/webapps/demo" || args[field] != "@chat/webapps/demo" || got["label"] != args["label"] {
				t.Fatalf("bad rewrite: %#v %#v", got, args)
			}
		}
	}
	got, err := resolveDesktopActionPaths(session, "desktop.theme.get", map[string]any{"projectPath": "@chat/no-rewrite"})
	if err != nil || got["projectPath"] != "@chat/no-rewrite" {
		t.Fatal(got, err)
	}
	got, err = resolveDesktopActionPaths(session, "desktop.webapp.install", map[string]any{"workspaceArchivePath": "releases/generated.zip"})
	if err != nil || got["workspaceArchivePath"] != "releases/generated.zip" {
		t.Fatal(got, err)
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
