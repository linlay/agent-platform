package tools

import (
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestDesktopControlRequiresExactOneShotApproval(t *testing.T) {
	for _, tc := range []struct {
		tool, action string
		params       map[string]any
	}{
		{"desktop_settings", "theme.set", map[string]any{"themeMode": "dark"}},
		{"desktop_settings", "pet.import", map[string]any{"filePath": "/tmp/panda.pet.zip"}},
		{"desktop_site", "website.add", map[string]any{"label": "Example", "url": "https://example.test"}},
		{"desktop_kanban", "kanban.createIssue", map[string]any{"input": map[string]any{"title": "Example"}}},
		{"desktop_webapp", "webapp.stop", map[string]any{"webappId": "app"}},
		{"desktop_shell", "runtime.diagnostics", map[string]any{}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			root := t.TempDir()
			executor, e, invoker := desktopCDPParamsTestRuntime(root)
			e.CurrentToolID = "install-call"
			owner, _ := connector.NativeToolConnector(tc.tool)
			e.Session.ConnectorDirs = map[string]string{owner: root}
			e.Session.NativeConnectorTools = map[string]string{tc.tool: owner}
			args := map[string]any{"action": tc.action, "args": tc.params}
			plan, err := executor.PrepareToolApproval(context.Background(), tc.tool, args, e)
			if err != nil || plan == nil || plan.View != nil || !plan.AllowAutoApprove {
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
			writes := 0
			for _, request := range requests {
				if request.Type == "desktop."+tc.action {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("dispatch count %d", len(requests))
			}
			if _, exists := requests[0].Payload["permissionMode"]; exists {
				t.Fatal("installation elevated Desktop permission mode")
			}
		})
	}
}

func TestDesktopControlPresentationAndHostOwnedActions(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		for _, a := range connector.ControlActions() {
			if a.Tool != def.Name || len(a.Tool) < 8 || a.Tool[:8] != "desktop_" {
				continue
			}
			selected, err := selectConfirmationRule(def.Meta["confirmationRules"], map[string]any{"action": a.Action})
			review := desktopControlReviewAction(a.Tool, a.Action)
			if err != nil || review && (selected == nil || selected.view.Key != desktopReviewExpectedKey(a.Action)) || !review && selected != nil {
				t.Fatalf("%s/%s: %#v %v", a.Tool, a.Action, selected, err)
			}
		}
	}
	executor, e, invoker := desktopCDPParamsTestRuntime(t.TempDir())
	for _, tc := range []struct{ tool, action string }{
		{"desktop_market", "market.installItem"}, {"desktop_market", "market.updateItem"}, {"desktop_market", "market.uninstallItem"},
		{"desktop_market", "market.exportSandboxImage"}, {"desktop_webapp", "webapp.install"}, {"desktop_webapp", "webapp.publish"},
	} {
		owner, _ := connector.NativeToolConnector(tc.tool)
		e.Session.ConnectorDirs = map[string]string{owner: t.TempDir()}
		e.Session.NativeConnectorTools = map[string]string{tc.tool: owner}
		args := map[string]any{"action": tc.action, "args": map[string]any{}}
		plan, err := executor.PrepareToolApproval(context.Background(), tc.tool, args, e)
		if err != nil || plan != nil {
			t.Fatalf("host-owned action acquired Platform review: %s %#v %v", tc.action, plan, err)
		}
		result, err := executor.Invoke(context.Background(), tc.tool, args, e)
		if err != nil || result.Error != "" {
			t.Fatalf("host-owned dispatch: %s %#v %v", tc.action, result, err)
		}
	}
	_, requests := invoker.snapshots()
	if len(requests) != 6 {
		t.Fatalf("host dispatches: %d", len(requests))
	}
}

// The paired Desktop checkout must exempt every reviewed action, or default
// mode would prompt twice. Market mutation must remain Desktop-owned.
func TestDesktopControlReviewsMatchDesktopExemptions(t *testing.T) {
	root := os.Getenv("DESKTOP_SOURCE")
	explicit := root != ""
	if !explicit {
		root = filepath.Join("..", "..", "..", "zenmind-desktop")
	}
	source, err := os.ReadFile(filepath.Join(root, "src", "main", "modules", "desktop-actions", "action-contracts.ts"))
	if !explicit && os.IsNotExist(err) {
		t.Skip("Desktop checkout absent")
	}
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)AGENT_PLATFORM_CONFIRMATION_EXEMPT_ACTIONS = new Set\(\[(.*?)\]\)`).FindSubmatch(source)
	if len(block) != 2 {
		t.Fatal("Desktop exemption list not found")
	}
	exempt := map[string]bool{}
	for _, m := range regexp.MustCompile(`"desktop\.([\w.]+)"`).FindAllSubmatch(block[1], -1) {
		exempt[string(m[1])] = true
	}
	for _, a := range connector.ControlActions() {
		if desktopControlReviewAction(a.Tool, a.Action) && !exempt[a.Action] {
			t.Errorf("double confirmation for %s", a.Action)
		}
		if a.Tool == "desktop_market" && !a.ReadOnly && a.Action != "market.refresh" && a.Action != "market.openItem" && exempt[a.Action] {
			t.Errorf("market resource management bypasses Desktop: %s", a.Action)
		}
	}
}

func desktopReviewExpectedKey(action string) string {
	switch {
	case action == "runtime.diagnostics":
		return "desktop_diagnostics_review"
	case action == "web.exportArtifact":
		return "desktop_export_review"
	case len(action) >= 7 && action[:7] == "webapp.":
		return "desktop_webapp_review"
	case len(action) >= 7 && action[:7] == "kanban.":
		return "desktop_kanban_review"
	case len(action) >= 8 && action[:8] == "website.":
		return "desktop_website_review"
	default:
		return "desktop_appearance_review"
	}
}

func TestDesktopReviewContextUsesOnlySelectedReadSnapshot(t *testing.T) {
	executor, e, _ := desktopCDPParamsTestRuntime(t.TempDir())
	e.CurrentToolID = "review"
	var actions []string
	invoker := &awcpClientRequestInvoker{response: func(request ClientRequest) map[string]any {
		actions = append(actions, request.Type)
		if request.Type != "desktop.website.list" {
			t.Fatalf("unexpected pre-approval action %s", request.Type)
		}
		return map[string]any{"ok": true, "result": map[string]any{"items": []any{
			map[string]any{"id": "other", "label": "UNRELATED", "secret": "not-for-review"},
			map[string]any{"id": "site", "label": "文档中心", "url": "https://old.test", "copilotAgentKey": "helper", "secret": "not-for-review"},
		}}}
	}}
	executor.clientRequest = invoker
	args := map[string]any{"action": "website.update", "args": map[string]any{"id": "site", "patch": map[string]any{"url": "https://new.test"}}}
	plan, err := executor.PrepareToolApproval(context.Background(), "desktop_site", args, e)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := plan.Form["current"].(map[string]any)
	if before["label"] != "文档中心" || before["url"] != "https://old.test" || len(actions) != 1 {
		t.Fatalf("context: %#v calls=%v", plan.Form, actions)
	}
	data, _ := json.Marshal(plan.Form)
	if regexp.MustCompile(`UNRELATED|not-for-review`).Match(data) {
		t.Fatalf("unrelated data leaked: %s", data)
	}
	if plan.Fingerprint != desktopControlFingerprint(e, "desktop_site", args) {
		t.Fatal("display context altered exact argument authorization")
	}
	// Failure leaves current values absent, never fabricated or substituted from a different target.
	invoker.response = func(request ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": map[string]any{"items": []any{map[string]any{"id": "other", "label": "wrong"}}}}
	}
	plan, err = executor.PrepareToolApproval(context.Background(), "desktop_site", args, e)
	if err != nil || plan.Form["current"] != nil {
		t.Fatalf("invented baseline: %#v %v", plan, err)
	}
}

func TestDesktopReviewNeverPrefetchesDiagnosticsAndSkipsAutoApprovalReads(t *testing.T) {
	executor, e, _ := desktopCDPParamsTestRuntime(t.TempDir())
	e.CurrentToolID = "review"
	executor.clientRequest = &awcpClientRequestInvoker{response: func(ClientRequest) map[string]any { t.Fatal("unexpected prefetch"); return nil }}
	for _, tc := range []struct{ tool, action, level string }{
		{"desktop_shell", "runtime.diagnostics", AccessLevelDefault},
		{"desktop_settings", "theme.set", AccessLevelAutoApprove},
		{"desktop_settings", "theme.set", AccessLevelFullAccess},
	} {
		e.AccessLevel = tc.level
		plan, err := executor.PrepareToolApproval(context.Background(), tc.tool, map[string]any{"action": tc.action, "args": map[string]any{}}, e)
		if err != nil || plan == nil || plan.Form["current"] != nil {
			t.Fatalf("%#v %v", plan, err)
		}
	}
}

func TestDesktopReviewAppAndSurfaceSnapshotsAreScoped(t *testing.T) {
	for _, tc := range []struct {
		action, key, id, read string
		value                 map[string]any
		want                  string
	}{
		{"webapp.restart", "id", "app-1", "desktop.site.list", map[string]any{"items": []any{
			map[string]any{"id": "other", "kind": "webapp", "label": "UNRELATED"},
			map[string]any{"id": "app-1", "kind": "webapp", "label": "反馈看板", "openMode": "dialog", "userConfig": map[string]any{"token": "PRIVATE"}},
		}}, "反馈看板"},
		{"web.exportArtifact", "surfaceId", "page-1", "desktop.web.getSurfaceState", map[string]any{"surface": map[string]any{"surfaceId": "page-1", "title": "周报", "url": "https://example.test", "containerId": "INTERNAL"}}, "周报"},
		{"web.exportArtifact", "surfaceId", "page-1", "desktop.web.getSurfaceState", map[string]any{"surface": map[string]any{"surfaceId": "page-other", "title": "UNRELATED"}}, ""},
	} {
		t.Run(tc.action+tc.want, func(t *testing.T) {
			executor, e, _ := desktopCDPParamsTestRuntime(t.TempDir())
			e.CurrentToolID = "review"
			executor.clientRequest = &awcpClientRequestInvoker{response: func(request ClientRequest) map[string]any {
				if request.Type != tc.read {
					t.Fatalf("unexpected read %s", request.Type)
				}
				return map[string]any{"ok": true, "result": tc.value}
			}}
			plan, err := executor.PrepareToolApproval(context.Background(), "desktop_webapp", map[string]any{"action": tc.action, "args": map[string]any{tc.key: tc.id}}, e)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(plan.Form)
			if regexp.MustCompile(`UNRELATED|PRIVATE|INTERNAL`).Match(data) {
				t.Fatalf("unselected data leaked: %s", data)
			}
			if tc.want == "" && plan.Form["current"] != nil {
				t.Fatal("wrong surface accepted")
			}
			if tc.want != "" && !regexp.MustCompile(tc.want).Match(data) {
				t.Fatalf("missing friendly name: %s", data)
			}
		})
	}
}
