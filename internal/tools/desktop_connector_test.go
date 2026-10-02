package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
)

func TestNativeToolDispatchRequiresMountWithoutConfiguration(t *testing.T) {
	executor, ctx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
	executor.cfg.Paths.StateDir = filepath.Join(t.TempDir(), "missing")
	ctx.Session.NativeConnectorTools = nil
	result, err := executor.Invoke(context.Background(), "surface_list", map[string]any{}, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("unmounted: %#v %v", result, err)
	}
	_, requests := invoker.snapshots()
	if len(requests) != 0 {
		t.Fatal("unmounted invocation reached Desktop")
	}
	ctx.Session.NativeConnectorTools = map[string]string{"surface_list": connector.WebControlConnectorID}
	result, err = executor.Invoke(context.Background(), " surface_list ", map[string]any{}, ctx)
	if err != nil || result.Error != "" {
		t.Fatalf("mounted: %#v %v", result, err)
	}
	_, requests = invoker.snapshots()
	if len(requests) != 1 || requests[0].Payload["method"] != "Surface.list" {
		t.Fatalf("expected Desktop dispatch: %#v", requests)
	}
	if _, err := os.Stat(executor.cfg.Paths.StateDir); !os.IsNotExist(err) {
		t.Fatal("dispatch created configuration state", err)
	}
}

func TestNativeToolsBelongToExactlyOneConnector(t *testing.T) {
	root := t.TempDir()
	executor, ctx, invoker := desktopCDPParamsTestRuntime(root)
	// Mounting Desktop alone grants no page tool, and a grant recorded for the
	// wrong connector is not honoured.
	ctx.Session.ConnectorDirs = map[string]string{connector.DesktopConnectorID: root}
	ctx.Session.NativeConnectorTools = map[string]string{"desktop_action": connector.DesktopConnectorID, "surface_list": connector.DesktopConnectorID}
	result, err := executor.Invoke(context.Background(), "surface_list", map[string]any{}, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("borrowed mount: %+v %v", result, err)
	}
	// desktop_action no longer reaches WorkPanel or page actions.
	for _, action := range []string{"desktop.workpanel.openWeb", "desktop.workpanel.getState", "desktop.web.listSurfaces", "desktop.web.executeScript"} {
		result, err = executor.Invoke(context.Background(), "desktop_action", map[string]any{"action": action, "args": map[string]any{}}, ctx)
		if err != nil || result.Error != "unknown_action" {
			t.Fatalf("%s: %+v %v", action, result, err)
		}
	}
	if _, requests := invoker.snapshots(); len(requests) != 0 {
		t.Fatalf("rejected calls reached Desktop: %#v", requests)
	}
	// web-control alone opens and controls pages but grants no Desktop action.
	ctx.Session.ConnectorDirs = map[string]string{connector.WebControlConnectorID: root}
	ctx.Session.NativeConnectorTools = mountedNativeToolsForTest()
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"workpanel_open", map[string]any{"url": "https://example.test"}},
		{"surface_list", map[string]any{}},
		{"awcp_manual", map[string]any{"surfaceId": "page:test"}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, ctx)
		if err != nil || result.Error != "" {
			t.Fatalf("%s %v: %+v %v", tc.tool, tc.args, result, err)
		}
	}
	result, err = executor.Invoke(context.Background(), "desktop_action", map[string]any{"action": "desktop.theme.get"}, ctx)
	if err != nil || result.Error != "connector_not_mounted" {
		t.Fatalf("desktop action without Desktop mount: %+v %v", result, err)
	}
	if _, requests := invoker.snapshots(); len(requests) != 3 {
		t.Fatalf("dispatch count: %d", len(requests))
	}
}
