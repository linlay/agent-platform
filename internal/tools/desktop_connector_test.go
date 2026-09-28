package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopNativeDispatchRequiresMountWithoutConfiguration(t *testing.T) {
	executor, ctx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
	executor.cfg.Paths.StateDir = filepath.Join(t.TempDir(), "missing")
	args := map[string]any{"method": "Surface.list"}
	ctx.Session.NativeConnectorTools = nil
	result, err := executor.Invoke(context.Background(), "desktop_cdp", args, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("unmounted: %#v %v", result, err)
	}
	_, requests := invoker.snapshots()
	if len(requests) != 0 {
		t.Fatal("unmounted invocation reached Desktop")
	}
	ctx.Session.NativeConnectorTools = map[string]string{"desktop_cdp": "builtin.desktop"}
	result, err = executor.Invoke(context.Background(), " desktop_cdp ", args, ctx)
	if err != nil || result.Error != "" {
		t.Fatalf("mounted: %#v %v", result, err)
	}
	_, requests = invoker.snapshots()
	if len(requests) != 1 {
		t.Fatalf("expected Desktop dispatch: %#v", requests)
	}
	if _, err := os.Stat(executor.cfg.Paths.StateDir); !os.IsNotExist(err) {
		t.Fatal("dispatch created configuration state", err)
	}
}

func TestDesktopWebDispatchSharesHandlersAndRequiresMatchingMount(t *testing.T) {
	root := t.TempDir()
	executor, ctx, invoker := desktopCDPParamsTestRuntime(root)
	ctx.Session.NativeConnectorTools = map[string]string{"desktop_action": "builtin.desktop-web", "desktop_cdp": "builtin.desktop-web"}
	// A grant for Web cannot borrow the full variant's directory.
	result, err := executor.Invoke(context.Background(), "desktop_cdp", map[string]any{"method": "Surface.list"}, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("mismatched mount: %+v %v", result, err)
	}
	ctx.Session.ConnectorDirs = map[string]string{"builtin.desktop-web": root}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"desktop_action", map[string]any{"action": "desktop.workpanel.openWeb", "args": map[string]any{"url": "https://example.test"}}},
		{"desktop_cdp", map[string]any{"method": "Surface.list"}},
		{"desktop_cdp", map[string]any{"method": "AWCP.getManual", "surfaceId": "page:test"}},
		// Skill scope is not an additional execution allowlist.
		{"desktop_action", map[string]any{"action": "desktop.theme.get"}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, ctx)
		if err != nil || result.Error != "" {
			t.Fatalf("%s %v: %+v %v", tc.tool, tc.args, result, err)
		}
	}
	_, requests := invoker.snapshots()
	if len(requests) != 4 {
		t.Fatalf("dispatch count: %d", len(requests))
	}
}
