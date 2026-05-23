package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestInvokeDesktopActionCallsBridge(t *testing.T) {
	var got desktopActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"action":"desktop.controlCenter.listServices","result":{"count":1}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor(server.URL, "").invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.controlCenter.listServices",
		"args": map[string]any{
			"include": "all",
		},
	}, &ExecutionContext{Session: QuerySession{
		RunID:    "run-1",
		ChatID:   "chat-1",
		AgentKey: "desktopAssistant",
	}})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Action != "desktop.controlCenter.listServices" {
		t.Fatalf("unexpected action: %s", got.Action)
	}
	if got.Args["include"] != "all" {
		t.Fatalf("unexpected args: %#v", got.Args)
	}
	if got.Source.RunID != "run-1" || got.Source.ChatID != "chat-1" || got.Source.AgentKey != "desktopAssistant" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
}

func TestInvokeDesktopCDPCallsBridge(t *testing.T) {
	var got desktopCDPRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"method":"Runtime.evaluate","result":{"value":42}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor("", server.URL).invokeDesktopCDP(context.Background(), map[string]any{
		"requestId": "req-cdp",
		"method":    "Runtime.evaluate",
		"targetId":  "target-1",
		"sessionId": "session-1",
		"surfaceId": "surface-1",
		"params": map[string]any{
			"expression": "6 * 7",
		},
	}, &ExecutionContext{Session: QuerySession{
		RunID:    "run-cdp",
		ChatID:   "chat-cdp",
		AgentKey: "desktopAssistant",
	}})
	if err != nil {
		t.Fatalf("invoke desktop cdp: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.RequestID != "req-cdp" || got.Method != "Runtime.evaluate" {
		t.Fatalf("unexpected cdp request: %#v", got)
	}
	if got.TargetID != "target-1" || got.SessionID != "session-1" || got.SurfaceID != "surface-1" {
		t.Fatalf("unexpected target routing: %#v", got)
	}
	if got.Params["expression"] != "6 * 7" {
		t.Fatalf("unexpected params: %#v", got.Params)
	}
	if got.Source.RunID != "run-cdp" || got.Source.ChatID != "chat-cdp" || got.Source.AgentKey != "desktopAssistant" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
}

func TestInvokeDesktopActionRejectsUnknownAction(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.unlisted.anything",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "unknown_action" {
		t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func TestInvokeDesktopActionRejectsPageActions(t *testing.T) {
	for _, action := range []string{
		"desktop.page.readCurrent",
		"desktop.embeddedWeb.readPageData",
	} {
		t.Run(action, func(t *testing.T) {
			result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
				"action": action,
			}, &ExecutionContext{})
			if err != nil {
				t.Fatalf("invoke desktop action: %v", err)
			}
			if result.ExitCode != -1 || result.Error != "unknown_action" {
				t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
			}
		})
	}
}

func TestDesktopActionAllowlistMatchesToolSchema(t *testing.T) {
	want := []string{
		"desktop.agents.applyAgentConfigPatch",
		"desktop.agents.cloneAgent",
		"desktop.agents.createAgent",
		"desktop.agents.createAgentDraft",
		"desktop.agents.disableAgent",
		"desktop.agents.getAgentDetail",
		"desktop.agents.listAgents",
		"desktop.agents.previewAgentConfigPatch",
		"desktop.agents.reloadAgents",
		"desktop.agents.updateAgent",
		"desktop.agents.validateAgentConfig",
		"desktop.automations.createAutomation",
		"desktop.automations.deleteAutomation",
		"desktop.automations.explainNextRun",
		"desktop.automations.getAutomationDetail",
		"desktop.automations.listAutomations",
		"desktop.automations.pauseAutomation",
		"desktop.automations.previewAutomation",
		"desktop.automations.resumeAutomation",
		"desktop.automations.updateAutomation",
		"desktop.automations.validateAutomation",
		"desktop.controlCenter.getServiceDetail",
		"desktop.controlCenter.getServiceLogsMeta",
		"desktop.controlCenter.getServiceStatus",
		"desktop.controlCenter.initializeService",
		"desktop.controlCenter.installService",
		"desktop.controlCenter.listServices",
		"desktop.controlCenter.openLogViewer",
		"desktop.controlCenter.readServiceLog",
		"desktop.controlCenter.restartService",
		"desktop.controlCenter.startService",
		"desktop.controlCenter.stopService",
		"desktop.help.explainCurrentPage",
		"desktop.help.getCurrentTopic",
		"desktop.help.navigateToRelatedPage",
		"desktop.help.openTopic",
		"desktop.help.searchTopics",
		"desktop.help.suggestNextAction",
		"desktop.market.applySettingsPatch",
		"desktop.market.buildSandboxImage",
		"desktop.market.getItemDetail",
		"desktop.market.getSettings",
		"desktop.market.importSkill",
		"desktop.market.installItem",
		"desktop.market.listItems",
		"desktop.market.previewSettingsPatch",
		"desktop.market.refresh",
		"desktop.market.uninstallItem",
		"desktop.market.updateItem",
		"desktop.market.validateSettings",
		"desktop.navigate.toRoute",
		"desktop.settings.applyPatch",
		"desktop.settings.getState",
		"desktop.settings.previewPatch",
		"desktop.settings.validatePatch",
	}
	sort.Strings(want)

	gotAllowlist := sortedDesktopActionAllowlist()
	if !reflect.DeepEqual(gotAllowlist, want) {
		t.Fatalf("desktop action allowlist mismatch\nwant: %#v\n got: %#v", want, gotAllowlist)
	}

	gotSchema := sortedToolPropertyEnum(t, "desktop_action", "action")
	if !reflect.DeepEqual(gotSchema, want) {
		t.Fatalf("desktop action schema enum mismatch\nwant: %#v\n got: %#v", want, gotSchema)
	}
}

func TestDesktopCDPMethodSchemaUsesRecommendedEnum(t *testing.T) {
	want := []string{
		"DOM.getBoxModel",
		"DOM.getDocument",
		"DOM.getOuterHTML",
		"DOM.querySelector",
		"DOM.querySelectorAll",
		"Input.dispatchKeyEvent",
		"Input.dispatchMouseEvent",
		"Input.insertText",
		"Network.disable",
		"Network.enable",
		"Page.bringToFront",
		"Page.captureScreenshot",
		"Page.enable",
		"Page.navigate",
		"Page.reload",
		"Runtime.evaluate",
		"Target.getCurrentTarget",
		"Target.getTargets",
	}
	sort.Strings(want)

	got := sortedToolPropertyEnum(t, "desktop_cdp", "method")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop_cdp method enum mismatch\nwant: %#v\n got: %#v", want, got)
	}
	for _, endpoint := range []string{"/json/version", "/json", "/json/list"} {
		if enumContainsString(got, endpoint) {
			t.Fatalf("desktop_cdp method enum must not include HTTP endpoint %q", endpoint)
		}
	}
}

func TestInvokeDesktopActionRequiresConfiguredBridge(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.controlCenter.listServices",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_action_bridge_not_configured" {
		t.Fatalf("expected bridge not configured failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func sortedDesktopActionAllowlist() []string {
	values := make([]string, 0, len(desktopActionAllowlist))
	for action := range desktopActionAllowlist {
		values = append(values, action)
	}
	sort.Strings(values)
	return values
}

func sortedToolPropertyEnum(t *testing.T, toolName string, propertyName string) []string {
	t.Helper()
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tools: %v", err)
	}
	for _, def := range defs {
		if def.Name != toolName {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s parameters missing properties: %#v", toolName, def.Parameters)
		}
		property, ok := properties[propertyName].(map[string]any)
		if !ok {
			t.Fatalf("%s property %s missing: %#v", toolName, propertyName, properties[propertyName])
		}
		enum, ok := property["enum"].([]any)
		if !ok {
			t.Fatalf("%s property %s missing enum: %#v", toolName, propertyName, property)
		}
		values := make([]string, 0, len(enum))
		for _, item := range enum {
			value, ok := item.(string)
			if !ok {
				t.Fatalf("%s property %s enum contains non-string: %#v", toolName, propertyName, item)
			}
			values = append(values, value)
		}
		sort.Strings(values)
		return values
	}
	t.Fatalf("tool %s not found", toolName)
	return nil
}

func enumContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func newDesktopTestExecutor(actionURL string, cdpURL string) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{cfg: config.Config{Desktop: config.DesktopConfig{
		Action: config.DesktopBridgeConfig{
			BridgeURL:        actionURL,
			RequestTimeoutMs: 20000,
		},
		CDP: config.DesktopBridgeConfig{
			BridgeURL:        cdpURL,
			RequestTimeoutMs: 20000,
		},
	}}}
}
