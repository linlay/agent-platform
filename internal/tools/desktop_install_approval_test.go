package tools

import (
	"agent-platform/internal/connector"
	"context"
	"testing"
)

func TestDesktopInstallationRequiresExactOneShotApproval(t *testing.T) {
	for _, tc := range []struct {
		tool, action string
		params       map[string]any
	}{
		{"desktop_market", "market.installItem", map[string]any{"itemId": "example"}},
		{"desktop_market", "market.updateItem", map[string]any{"itemId": "example"}},
		{"desktop_webapp", "webapp.install", map[string]any{"workspaceArchivePath": "dist/app.zip", "expectedId": "app"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			root := t.TempDir()
			executor, e, invoker := desktopCDPParamsTestRuntime(root)
			e.CurrentToolID = "install-call"
			e.Session.ConnectorDirs = map[string]string{connector.PlatformControlConnectorID: root}
			e.Session.NativeConnectorTools = map[string]string{tc.tool: connector.PlatformControlConnectorID}
			args := map[string]any{"action": tc.action, "args": tc.params}
			plan, err := executor.PrepareToolApproval(context.Background(), tc.tool, args, e)
			if err != nil || plan == nil || plan.ViewportKey != "" {
				t.Fatalf("business planner: %#v %v", plan, err)
			}
			result, err := executor.Invoke(context.Background(), tc.tool, args, e)
			if err != nil || result.Error != "approval_required" {
				t.Fatalf("unapproved: %#v %v", result, err)
			}
			e.ToolApprovals = map[string]bool{plan.Fingerprint: true}
			sibling := *e
			sibling.CurrentToolID = "sibling"
			result, err = executor.Invoke(context.Background(), tc.tool, args, &sibling)
			if err != nil || result.Error != "approval_required" {
				t.Fatal("sibling borrowed approval")
			}
			changed := map[string]any{"action": tc.action, "args": map[string]any{"itemId": "other", "workspaceArchivePath": "other.zip"}}
			result, err = executor.Invoke(context.Background(), tc.tool, changed, e)
			if err != nil || result.Error != "approval_required" {
				t.Fatalf("changed arguments borrowed approval: %#v %v", result, err)
			}
			result, err = executor.Invoke(context.Background(), tc.tool, args, e)
			if err != nil || result.Error != "" {
				t.Fatalf("approved: %#v %v", result, err)
			}
			result, err = executor.Invoke(context.Background(), tc.tool, args, e)
			if err != nil || result.Error != "approval_required" {
				t.Fatal("approval reused")
			}
			_, requests := invoker.snapshots()
			if len(requests) != 1 {
				t.Fatalf("dispatch count %d", len(requests))
			}
			if _, exists := requests[0].Payload["permissionMode"]; exists {
				t.Fatal("installation elevated Desktop permission mode")
			}
		})
	}
}

func TestInstallationPresentationComesFromToolYAML(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		var actions []string
		if def.Name == "desktop_market" {
			actions = []string{"market.installItem", "market.updateItem"}
		}
		if def.Name == "desktop_webapp" {
			actions = []string{"webapp.install"}
		}
		for _, action := range actions {
			selected, err := selectConfirmationRule(def.Meta["confirmationRules"], map[string]any{"action": action})
			if err != nil || selected == nil || selected.viewportKey != "installation_review" {
				t.Fatalf("missing config %s %v", action, err)
			}
		}
		if len(actions) > 0 {
			selected, err := selectConfirmationRule(def.Meta["confirmationRules"], map[string]any{"action": "unrelated"})
			if err != nil || selected != nil {
				t.Fatal("unrelated action matched")
			}
		}
	}
}
