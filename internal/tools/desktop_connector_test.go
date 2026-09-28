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
