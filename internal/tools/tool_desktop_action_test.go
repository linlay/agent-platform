package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
	response := result.Structured["response"].(map[string]any)
	resultNode := response["result"].(map[string]any)
	if resultNode["value"] != float64(42) {
		t.Fatalf("unexpected structured cdp result: %#v", result.Structured)
	}
}

func TestInvokeDesktopCDPNormalizesStringBooleanParams(t *testing.T) {
	var got desktopCDPRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"method":"Runtime.evaluate","result":{"value":42}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor("", server.URL).invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Runtime.evaluate",
		"params": map[string]any{
			"expression":    "document.title",
			"returnByValue": "true",
			"awaitPromise":  "false",
		},
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop cdp: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Params["expression"] != "document.title" {
		t.Fatalf("expression should remain a string, got %#v", got.Params["expression"])
	}
	if got.Params["returnByValue"] != true {
		t.Fatalf("returnByValue should be boolean true, got %#v", got.Params["returnByValue"])
	}
	if got.Params["awaitPromise"] != false {
		t.Fatalf("awaitPromise should be boolean false, got %#v", got.Params["awaitPromise"])
	}
}

func TestInvokeDesktopCDPCaptureScreenshotSavesImageAndOmitsBase64(t *testing.T) {
	png := testDesktopScreenshotPNG(t)
	encoded := base64.StdEncoding.EncodeToString(png)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"method": "Page.captureScreenshot",
			"result": map[string]any{"data": encoded},
		})
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = chatsDir
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
		"params": map[string]any{"format": "png"},
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful screenshot save, got exit=%d output=%s", result.ExitCode, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("screenshot base64 leaked into output: %s", result.Output)
	}

	response := result.Structured["response"].(map[string]any)
	resultNode := response["result"].(map[string]any)
	data := resultNode["data"].(map[string]any)
	referenceName := data["referenceName"].(string)
	if !strings.HasPrefix(referenceName, "desktop-cdp-screenshot-") || !strings.HasSuffix(referenceName, ".png") {
		t.Fatalf("unexpected reference name: %q", referenceName)
	}
	filePath := data["filePath"].(string)
	if filePath != filepath.Join(chatsDir, "chat-cdp", referenceName) {
		t.Fatalf("unexpected file path: %q", filePath)
	}
	written, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read written screenshot: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Fatalf("written screenshot bytes differ")
	}
	sha := sha256.Sum256(png)
	if data["sha256"] != hex.EncodeToString(sha[:]) || data["mimeType"] != "image/png" || data["sizeBytes"] != len(png) {
		t.Fatalf("unexpected screenshot metadata: %#v", data)
	}
	if data["saved"] != true || data["dataOmitted"] != true {
		t.Fatalf("expected saved/dataOmitted flags: %#v", data)
	}
	visionImage := data["visionRecognizeImage"].(map[string]any)
	if visionImage["reference_name"] != referenceName {
		t.Fatalf("unexpected vision image payload: %#v", visionImage)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRequiresChatContext(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testDesktopScreenshotPNG(t))
	server := desktopScreenshotServer(t, encoded, true)
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_context_unavailable" {
		t.Fatalf("expected context error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("screenshot base64 leaked into context error output: %s", result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRejectsInvalidBase64(t *testing.T) {
	encoded := "not-valid-base64-data-that-must-not-leak"
	server := desktopScreenshotServer(t, encoded, true)
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_decode_failed" {
		t.Fatalf("expected decode error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("invalid screenshot base64 leaked into output: %s", result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRequiresResultData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"method": "Page.captureScreenshot",
			"result": map[string]any{},
		})
	}))
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_data_missing" {
		t.Fatalf("expected data missing error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotDoesNotSaveBridgeFailure(t *testing.T) {
	server := desktopScreenshotServer(t, "", false)
	defer server.Close()

	chatsDir := t.TempDir()
	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = chatsDir
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("expected bridge failure exit code, got %d output=%s", result.ExitCode, result.Output)
	}
	if _, err := os.Stat(filepath.Join(chatsDir, "chat-cdp")); !os.IsNotExist(err) {
		t.Fatalf("expected no screenshot directory on bridge failure, stat err=%v", err)
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

func TestInvokeDesktopActionAllowsCurrentDesktopActions(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got desktopActionRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requested = append(requested, got.Action)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"action": got.Action,
			"result": map[string]any{"ok": true},
		})
	}))
	defer server.Close()

	for _, action := range []string{
		"desktop.setting.getState",
		"desktop.web.listSurfaces",
		"desktop.web.webapp.getStatus",
		"desktop.web.website.list",
		"desktop.pet.show",
		"desktop.pet.state",
	} {
		t.Run(action, func(t *testing.T) {
			result, err := newDesktopTestExecutor(server.URL, "").invokeDesktopAction(context.Background(), map[string]any{
				"action": action,
			}, &ExecutionContext{})
			if err != nil {
				t.Fatalf("invoke desktop action: %v", err)
			}
			if result.ExitCode != 0 {
				t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
			}
		})
	}
	if len(requested) != 6 {
		t.Fatalf("expected bridge to receive 6 actions, got %d: %#v", len(requested), requested)
	}
}

func TestInvokeDesktopActionRejectsLegacyAndUnsupportedActions(t *testing.T) {
	for _, action := range []string{
		"desktop.settings.getState",
		"desktop.agents.listAgents",
		"desktop.automations.listAutomations",
		"desktop.help.searchTopics",
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
		"desktop.help.openTopic",
		"desktop.kanban.createIssue",
		"desktop.kanban.deleteIssue",
		"desktop.kanban.getIssue",
		"desktop.kanban.listIssues",
		"desktop.kanban.moveIssue",
		"desktop.kanban.updateIssue",
		"desktop.market.applySettingsPatch",
		"desktop.market.deleteSandboxImage",
		"desktop.market.exportSandboxImage",
		"desktop.market.getItemDetail",
		"desktop.market.getSettings",
		"desktop.market.importSandboxImage",
		"desktop.market.importSkill",
		"desktop.market.installItem",
		"desktop.market.listItems",
		"desktop.market.previewSettingsPatch",
		"desktop.market.refresh",
		"desktop.market.uninstallItem",
		"desktop.market.updateItem",
		"desktop.market.validateSettings",
		"desktop.navigate.toRoute",
		"desktop.pet.hide",
		"desktop.pet.list",
		"desktop.pet.set",
		"desktop.pet.show",
		"desktop.pet.state",
		"desktop.setting.applyPatch",
		"desktop.setting.getState",
		"desktop.setting.previewPatch",
		"desktop.setting.validatePatch",
		"desktop.web.activateSurface",
		"desktop.web.closeTab",
		"desktop.web.getActiveSurface",
		"desktop.web.goBack",
		"desktop.web.list",
		"desktop.web.listSurfaces",
		"desktop.web.navigate",
		"desktop.web.openTab",
		"desktop.web.reload",
		"desktop.web.switchTab",
		"desktop.web.webapp.getStatus",
		"desktop.web.webapp.installAndOpen",
		"desktop.web.webapp.open",
		"desktop.web.webapp.restart",
		"desktop.web.webapp.start",
		"desktop.web.webapp.stop",
		"desktop.web.website.add",
		"desktop.web.website.list",
		"desktop.web.website.remove",
		"desktop.web.website.update",
	}
	sort.Strings(want)

	gotAllowlist := sortedDesktopActionAllowlist(t)
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

func sortedDesktopActionAllowlist(t *testing.T) []string {
	t.Helper()
	allowlist, err := getDesktopActionAllowlist()
	if err != nil {
		t.Fatalf("load desktop action allowlist: %v", err)
	}
	values := make([]string, 0, len(allowlist))
	for action := range allowlist {
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

func testDesktopScreenshotPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode test png: %v", err)
	}
	return data
}

func desktopScreenshotServer(t *testing.T, data string, ok bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"ok":     ok,
			"method": "Page.captureScreenshot",
		}
		if ok {
			payload["result"] = map[string]any{"data": data}
		} else {
			w.WriteHeader(http.StatusBadRequest)
			payload["error"] = map[string]any{"code": "cdp_failed", "message": "capture failed"}
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func newDesktopTestExecutor(actionURL string, cdpURL string) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{cfg: config.Config{Desktop: config.DesktopConfig{
		Action: config.DesktopBridgeConfig{
			BridgeURL:      actionURL,
			RequestTimeout: 20,
		},
		CDP: config.DesktopBridgeConfig{
			BridgeURL:      cdpURL,
			RequestTimeout: 20,
		},
	}}}
}
