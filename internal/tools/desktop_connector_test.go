package tools

import (
	"agent-platform/internal/connector"
	"context"
	"testing"
)

func TestDesktopNativeDispatchRequiresMountAndConfigured(t *testing.T) {
	executor, ctx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
	args := map[string]any{"method": "Surface.list"}
	ctx.Session.NativeConnectorTools = nil
	result, err := executor.Invoke(context.Background(), "desktop_cdp", args, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("unmounted: %#v %v", result, err)
	}
	ctx.Session.NativeConnectorTools = map[string]string{"desktop_cdp": "builtin.desktop"}
	pkg := connector.Package{Manifest: connector.Manifest{ID: "builtin.desktop"}, StateRoot: executor.cfg.Paths.EffectiveConnectorStateDir()}
	if _, err := pkg.SetConfigured(false); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Invoke(context.Background(), " desktop_cdp ", args, ctx)
	if err != nil || result.Error != "connector_not_configured" {
		t.Fatalf("disconnected: %#v %v", result, err)
	}
	_, requests := invoker.snapshots()
	if len(requests) != 0 {
		t.Fatal("blocked invocation reached Desktop")
	}
}
