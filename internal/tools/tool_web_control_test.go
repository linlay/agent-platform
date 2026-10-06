package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

type webControlTestInvoker struct {
	requests []ClientRequest
	respond  func(ClientRequest) map[string]any
}

func (i *webControlTestInvoker) InvokeClientRequest(_ context.Context, _ ClientTarget, request ClientRequest, onFrame func(ClientResponseFrame) error) error {
	i.requests = append(i.requests, request)
	response := map[string]any{"ok": true, "result": map[string]any{}}
	if i.respond != nil {
		if scripted := i.respond(request); scripted != nil {
			response = scripted
		}
	}
	switch request.Type {
	case desktopAwcpManualAction:
		if _, ok := response["revision"]; !ok {
			response = map[string]any{"ok": true, "method": desktopAwcpGetManualMethod, "revision": "manual:1", "site": map[string]any{"name": "Test", "description": "Test"}, "sections": []any{}}
			if section, selected := request.Payload["section"]; selected {
				response["section"] = section
				response["revision"] = request.Payload["revision"]
			}
		}
	case desktopAwcpInvokeAction:
		response["requestId"] = request.ID
		response["action"] = request.Payload["action"]
	case desktopCDPRequestType:
		response["method"] = request.Payload["method"]
	default:
		response["action"] = request.Type
	}
	data, _ := json.Marshal(response)
	code := 0
	return onFrame(ClientResponseFrame{Frame: "response", Type: request.Type, ID: request.ID, Code: &code, Data: data})
}

func webControlTestRuntime(t *testing.T, respond func(ClientRequest) map[string]any) (*RuntimeToolExecutor, *ExecutionContext, *webControlTestInvoker) {
	t.Helper()
	root := t.TempDir()
	invoker := &webControlTestInvoker{respond: respond}
	executor, execCtx, _ := desktopCDPParamsTestRuntime(root)
	executor.clientRequest = invoker
	return executor, execCtx, invoker
}

func webControlWorkspace(items ...map[string]any) map[string]any {
	raw := make([]any, 0, len(items))
	for _, item := range items {
		raw = append(raw, item)
	}
	active := any(nil)
	if len(items) > 0 {
		active = items[len(items)-1]["itemId"]
	}
	return map[string]any{"workspaceId": "ws-1", "ownerChatId": "chat-1", "items": raw, "activeItemId": active}
}

func webControlWebItem(id, url string) map[string]any {
	return map[string]any{"itemId": id, "stableKey": "web:" + url, "descriptor": map[string]any{"kind": "web", "url": url}, "title": "Page", "closable": true, "pinned": false, "createdAt": 1}
}

func webControlFileItem(id, name, relative string) map[string]any {
	descriptor := map[string]any{"kind": "local-file", "handleId": "handle-" + id, "fileName": name, "previewKind": "html"}
	if relative != "" {
		descriptor["workspaceRelativePath"] = relative
	}
	return map[string]any{"itemId": id, "stableKey": "local-file:" + id, "descriptor": descriptor, "title": name, "closable": true, "pinned": false, "createdAt": 1}
}

func assertNoWebControlInternalIdentity(t *testing.T, result ToolExecutionResult) {
	t.Helper()
	for _, hidden := range []string{"itemId", "stableKey", "activeItemId", "workspaceId", "ownerChatId", "containerId", "closedItemId", "item-"} {
		if strings.Contains(result.Output, hidden) {
			t.Fatalf("result exposes %s: %s", hidden, result.Output)
		}
	}
	encoded, _ := json.Marshal(result.Structured)
	if string(encoded) != result.Output {
		t.Fatalf("output and structured result diverged:\n%s\n%s", result.Output, encoded)
	}
}

func TestWorkPanelOpenRoutesByURLPrefix(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
		if request.Type == "desktop.workpanel.openWeb" {
			return map[string]any{"ok": true, "result": map[string]any{
				"workspace": webControlWorkspace(webControlWebItem("item-1", "http://localhost:3000/")),
				"surfaceId": "page:1", "containerId": "container:1", "status": "loading",
			}}
		}
		return map[string]any{"ok": true, "result": map[string]any{"workspace": webControlWorkspace(webControlFileItem("item-2", "report.html", "artifacts/report.html"))}}
	})
	root := execCtx.Session.WorkspaceRoot
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "artifacts", "report.html"), "<html></html>")

	web, err := executor.Invoke(context.Background(), "workpanel_open", map[string]any{"url": "http://localhost:3000"}, execCtx)
	if err != nil || web.ExitCode != 0 {
		t.Fatalf("open web: %#v %v", web, err)
	}
	assertNoWebControlInternalIdentity(t, web)
	response := web.Structured["response"].(map[string]any)["result"].(map[string]any)
	wantPanel := map[string]any{"items": []any{map[string]any{"kind": "web", "url": "http://localhost:3000/", "title": "Page", "closable": true, "pinned": false, "active": true}}}
	if response["surfaceId"] != "page:1" || response["status"] != "loading" || !reflect.DeepEqual(response["workspace"], wantPanel) {
		t.Fatalf("web result: %#v", response)
	}

	file, err := executor.Invoke(context.Background(), "workpanel_open", map[string]any{"url": "@workspace/artifacts/report.html", "title": "Report"}, execCtx)
	if err != nil || file.ExitCode != 0 {
		t.Fatalf("open file: %#v %v", file, err)
	}
	assertNoWebControlInternalIdentity(t, file)
	if strings.Contains(file.Output, "surfaceId") || !strings.Contains(file.Output, `"url":"@workspace/artifacts/report.html"`) {
		t.Fatalf("file preview result: %s", file.Output)
	}

	if len(invoker.requests) != 2 {
		t.Fatalf("requests: %#v", invoker.requests)
	}
	if invoker.requests[0].Type != "desktop.workpanel.openWeb" || !reflect.DeepEqual(invoker.requests[0].Payload, map[string]any{"url": "http://localhost:3000"}) {
		t.Fatalf("web request: %#v", invoker.requests[0])
	}
	if invoker.requests[1].Type != "desktop.workpanel.openLocalFile" || !reflect.DeepEqual(invoker.requests[1].Payload, map[string]any{"path": "artifacts/report.html", "title": "Report"}) {
		t.Fatalf("file request: %#v", invoker.requests[1])
	}
	if invoker.requests[0].Source == nil || invoker.requests[0].Source.ChatID != execCtx.Session.ChatID {
		t.Fatalf("trusted source lost: %#v", invoker.requests[0].Source)
	}
}

func TestWorkPanelOpenRejectsAmbiguousTargetsBeforeSending(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for name, args := range map[string]map[string]any{
		"missing url":           {},
		"bare path":             {"url": "artifacts/report.html"},
		"host without scheme":   {"url": "localhost:3000"},
		"domain without scheme": {"url": "example.com/docs"},
		"file scheme":           {"url": "file:///tmp/report.html"},
		"absolute path":         {"url": "/tmp/report.html"},
		"credentials":           {"url": "https://user:secret@example.com/"},
		"alias without file":    {"url": "@workspace/"},
		"alias traversal":       {"url": "@workspace/../outside.html"},
		"unsupported alias":     {"url": "@temp/report.html"},
		"scheme alias":          {"url": "@workspace://report.html"},
		"web title":             {"url": "https://example.com/", "title": "Example"},
		"unknown field":         {"url": "https://example.com/", "kind": "web"},
		"item identity":         {"url": "https://example.com/", "tabId": "item-1"},
		"non-boolean reload":    {"url": "https://example.com/", "reload": "true"},
	} {
		result, err := executor.Invoke(context.Background(), "workpanel_open", args, execCtx)
		if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
			t.Errorf("%s: %#v %v", name, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("rejected input reached the client: %#v", invoker.requests)
	}
}

func TestWorkPanelOpenReloadRefreshesAfterOpening(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": map[string]any{"workspace": webControlWorkspace(webControlWebItem("item-1", "https://example.com/")), "surfaceId": "page:1", "containerId": "container:1", "status": "ready"}}
	})
	result, err := executor.Invoke(context.Background(), "workpanel_open", map[string]any{"url": "https://example.com/", "reload": true}, execCtx)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, `"surfaceId":"page:1"`) {
		t.Fatalf("reload open: %#v %v", result, err)
	}
	if len(invoker.requests) != 2 || invoker.requests[0].Type != "desktop.workpanel.openWeb" || invoker.requests[1].Type != "desktop.workpanel.refreshWeb" {
		t.Fatalf("requests: %#v", invoker.requests)
	}
	if !reflect.DeepEqual(invoker.requests[1].Payload, map[string]any{"url": "https://example.com/"}) {
		t.Fatalf("refresh payload: %#v", invoker.requests[1].Payload)
	}
}

func TestWorkPanelFilePreviewRequiresDesktopRuntime(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	executor.cfg.RuntimeMode = config.RuntimeModeStandalone
	execCtx.Session.WebClientTarget = ClientTarget{SessionID: "webclient"}
	mustWriteFile(t, filepath.Join(execCtx.Session.WorkspaceRoot, "report.html"), "<html></html>")
	result, err := executor.Invoke(context.Background(), "workpanel_open", map[string]any{"url": "@workspace/report.html"}, execCtx)
	if err != nil || result.Error != "desktop_action_unsupported_runtime" || len(invoker.requests) != 0 {
		t.Fatalf("standalone file preview: %#v %v %#v", result, err, invoker.requests)
	}
	// Surface and AWCP tools are Desktop-only; the WorkPanel itself is not.
	for _, tool := range []string{"surface_list", "awcp_manual"} {
		result, err = executor.Invoke(context.Background(), tool, map[string]any{}, execCtx)
		if err != nil || result.Error != "desktop_cdp_unsupported_runtime" || len(invoker.requests) != 0 {
			t.Fatalf("standalone %s: %#v %v", tool, result, err)
		}
	}
}

func TestWorkPanelStateHidesItemAndContainerIdentity(t *testing.T) {
	executor, execCtx, _ := webControlTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": map[string]any{"workspaceId": "ws-1", "state": webControlWorkspace(
			webControlWebItem("item-1", "https://example.com/"),
			webControlFileItem("item-2", "notes.pdf", ""),
			webControlFileItem("item-3", "report.html", "artifacts/report.html"),
		)}}
	})
	result, err := executor.Invoke(context.Background(), "workpanel_state", map[string]any{}, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("state: %#v %v", result, err)
	}
	assertNoWebControlInternalIdentity(t, result)
	state := result.Structured["response"].(map[string]any)["result"].(map[string]any)["state"].(map[string]any)
	want := []any{
		map[string]any{"kind": "web", "url": "https://example.com/", "title": "Page", "closable": true, "pinned": false, "active": false},
		map[string]any{"kind": "file", "fileName": "notes.pdf", "title": "notes.pdf", "closable": true, "pinned": false, "active": false},
		map[string]any{"kind": "file", "url": "@workspace/artifacts/report.html", "fileName": "report.html", "title": "report.html", "closable": true, "pinned": false, "active": true},
	}
	if !reflect.DeepEqual(state["items"], want) {
		t.Fatalf("items: %#v", state["items"])
	}
}

func TestWorkPanelCloseRequiresRecordedFilePath(t *testing.T) {
	workspace := webControlWorkspace(
		webControlWebItem("item-web", "https://example.com/docs"),
		webControlFileItem("item-html", "report.html", "artifacts/report.html"),
		webControlFileItem("item-pdf", "notes.pdf", ""),
	)
	respond := func(request ClientRequest) map[string]any {
		if request.Type == "desktop.workpanel.getState" {
			return map[string]any{"ok": true, "result": map[string]any{"workspaceId": "ws-1", "state": workspace}}
		}
		return map[string]any{"ok": true, "result": map[string]any{"closedItemId": request.Payload["tabId"], "workspace": webControlWorkspace()}}
	}
	for url, wantItem := range map[string]string{
		"@workspace/artifacts/report.html": "item-html",
	} {
		executor, execCtx, invoker := webControlTestRuntime(t, respond)
		result, err := executor.Invoke(context.Background(), "workpanel_close", map[string]any{"url": url}, execCtx)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%s: %#v %v", url, result, err)
		}
		assertNoWebControlInternalIdentity(t, result)
		if len(invoker.requests) != 2 || invoker.requests[1].Type != "desktop.workpanel.closeTab" || !reflect.DeepEqual(invoker.requests[1].Payload, map[string]any{"tabId": wantItem}) {
			t.Fatalf("%s requests: %#v", url, invoker.requests)
		}
	}

	executor, execCtx, invoker := webControlTestRuntime(t, respond)
	result, err := executor.Invoke(context.Background(), "workpanel_close", map[string]any{"url": "@workspace/other.html"}, execCtx)
	if err != nil || result.Error != "workpanel_item_not_found" || len(invoker.requests) != 1 {
		t.Fatalf("missing item: %#v %v %#v", result, err, invoker.requests)
	}
	assertNoWebControlInternalIdentity(t, result)

	// Even a unique basename does not prove that the requested file is open.
	executor, execCtx, invoker = webControlTestRuntime(t, respond)
	result, err = executor.Invoke(context.Background(), "workpanel_close", map[string]any{"url": "@workspace/docs/notes.pdf"}, execCtx)
	if err != nil || result.Error != "workpanel_item_not_found" || len(invoker.requests) != 1 {
		t.Fatalf("basename fallback: %#v %v %#v", result, err, invoker.requests)
	}

	// A file name shared by several previews cannot identify one item.
	workspace = webControlWorkspace(webControlFileItem("item-a", "notes.pdf", ""), webControlFileItem("item-b", "notes.pdf", ""))
	executor, execCtx, invoker = webControlTestRuntime(t, respond)
	result, err = executor.Invoke(context.Background(), "workpanel_close", map[string]any{"url": "@workspace/notes.pdf"}, execCtx)
	if err != nil || result.Error != "workpanel_item_not_found" || len(invoker.requests) != 1 {
		t.Fatalf("ambiguous item: %#v %v %#v", result, err, invoker.requests)
	}
}

func TestWorkPanelCloseRequiresExactlyOneSelector(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for name, args := range map[string]map[string]any{
		"empty":     {},
		"both":      {"url": "https://example.com/", "all": true},
		"all false": {"all": false},
		"item id":   {"tabId": "item-1"},
		"web url":   {"url": "https://example.com:443/"},
	} {
		result, err := executor.Invoke(context.Background(), "workpanel_close", args, execCtx)
		if err != nil || result.Error != "invalid_args" {
			t.Errorf("%s: %#v %v", name, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("rejected close reached the client: %#v", invoker.requests)
	}
	result, err := executor.Invoke(context.Background(), "workpanel_close", map[string]any{"all": true}, execCtx)
	if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 || invoker.requests[0].Type != "desktop.workpanel.closeWorkpanel" || len(invoker.requests[0].Payload) != 0 {
		t.Fatalf("close all: %#v %v %#v", result, err, invoker.requests)
	}
}

func TestSurfaceToolsMapToOneRequestEach(t *testing.T) {
	for _, tc := range []struct {
		tool       string
		args       map[string]any
		method     string
		surfaceID  string
		params     map[string]any
		actionType string
	}{
		{tool: "surface_list", args: map[string]any{}, method: "Surface.list"},
		{tool: "surface_state", args: map[string]any{}, method: "Surface.getCurrent"},
		{tool: "surface_state", args: map[string]any{"surfaceId": "page:1"}, method: "Surface.getState", surfaceID: "page:1"},
		{tool: "surface_navigate", args: map[string]any{"surfaceId": "page:1", "action": "goto", "url": "https://example.com/next"}, method: "Page.navigate", surfaceID: "page:1", params: map[string]any{"url": "https://example.com/next"}},
		{tool: "surface_navigate", args: map[string]any{"surfaceId": "page:1", "action": "reload", "ignoreCache": true}, method: "Page.reload", surfaceID: "page:1", params: map[string]any{"ignoreCache": true}},
		{tool: "surface_navigate", args: map[string]any{"surfaceId": "page:1", "action": "reload"}, method: "Page.reload", surfaceID: "page:1"},
		{tool: "surface_navigate", args: map[string]any{"surfaceId": "page:1", "action": "back"}, method: "Surface.goBack", surfaceID: "page:1"},
		{tool: "surface_activate", args: map[string]any{"surfaceId": "page:1"}, method: "Page.bringToFront", surfaceID: "page:1"},
		{tool: "surface_close", args: map[string]any{"surfaceId": "page:1"}, method: "Surface.close", surfaceID: "page:1"},
		{tool: "surface_evaluate", args: map[string]any{"surfaceId": "page:1", "expression": "document.title"}, method: "Runtime.evaluate", surfaceID: "page:1", params: map[string]any{"expression": "document.title", "returnByValue": true, "awaitPromise": true}},
		{tool: "surface_evaluate", args: map[string]any{"surfaceId": "page:1", "expression": "1", "awaitPromise": false}, method: "Runtime.evaluate", surfaceID: "page:1", params: map[string]any{"expression": "1", "returnByValue": true, "awaitPromise": false}},
		{tool: "surface_cdp", args: map[string]any{"surfaceId": "page:1", "method": "DOM.querySelector", "params": map[string]any{"nodeId": 1.0, "selector": "#a"}}, method: "DOM.querySelector", surfaceID: "page:1", params: map[string]any{"nodeId": 1.0, "selector": "#a"}},
		{tool: "surface_cdp", args: map[string]any{"surfaceId": "page:1", "method": "Network.enable"}, method: "Network.enable", surfaceID: "page:1"},
		{tool: "surface_element", args: map[string]any{"surfaceId": "page:1", "selector": "#name", "action": "fill", "value": "Ada"}, actionType: "desktop.web.interactElement", params: map[string]any{"surfaceId": "page:1", "selector": "#name", "action": "fill", "value": "Ada"}},
	} {
		executor, execCtx, invoker := webControlTestRuntime(t, func(ClientRequest) map[string]any {
			return map[string]any{"ok": true, "result": map[string]any{"surface": map[string]any{"surfaceId": "page:1", "containerId": "container:1", "url": "https://example.com/"}}}
		})
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%s %v: %#v %v", tc.tool, tc.args, result, err)
		}
		if len(invoker.requests) != 1 {
			t.Fatalf("%s sent %d requests", tc.tool, len(invoker.requests))
		}
		request := invoker.requests[0]
		if tc.actionType != "" {
			if request.Type != tc.actionType || !reflect.DeepEqual(request.Payload, tc.params) {
				t.Fatalf("%s action request: %#v", tc.tool, request)
			}
			continue
		}
		if request.Type != desktopCDPRequestType || request.Payload["method"] != tc.method {
			t.Fatalf("%s request: %#v", tc.tool, request)
		}
		gotSurface, _ := request.Payload["surfaceId"].(string)
		if gotSurface != tc.surfaceID {
			t.Fatalf("%s surfaceId = %q, want %q", tc.tool, gotSurface, tc.surfaceID)
		}
		gotParams, _ := request.Payload["params"].(map[string]any)
		if len(gotParams) != len(tc.params) || (len(tc.params) > 0 && !reflect.DeepEqual(gotParams, tc.params)) {
			t.Fatalf("%s params = %#v, want %#v", tc.tool, gotParams, tc.params)
		}
		// Lifecycle results never expose the container; page-owned data is untouched.
		lifecycle := tc.method != "Runtime.evaluate" && !strings.HasPrefix(tc.method, "DOM.") && !strings.HasPrefix(tc.method, "Network.")
		if lifecycle == strings.Contains(result.Output, "containerId") {
			t.Fatalf("%s container visibility: %s", tc.tool, result.Output)
		}
	}
}

func TestSurfaceToolsRejectInvalidInputBeforeSending(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"surface_list", map[string]any{"surfaceId": "page:1"}},
		{"surface_navigate", map[string]any{"action": "reload"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "open"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "goto"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "goto", "url": "example.com"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "goto", "url": "@workspace/a.html"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "reload", "url": "https://example.com/"}},
		{"surface_navigate", map[string]any{"surfaceId": "page:1", "action": "back", "ignoreCache": true}},
		{"surface_activate", map[string]any{}},
		{"surface_close", map[string]any{"surfaceId": "  "}},
		{"surface_close", map[string]any{"surfaceId": "page:1", "url": "https://example.com/"}},
		{"surface_screenshot", map[string]any{"surfaceId": "page:1", "format": "jpeg"}},
		{"surface_evaluate", map[string]any{"surfaceId": "page:1"}},
		{"surface_evaluate", map[string]any{"surfaceId": "page:1", "expression": "1", "expressionFile": "a.js"}},
		{"surface_evaluate", map[string]any{"surfaceId": "page:1", "expression": "  "}},
		{"surface_click", map[string]any{"surfaceId": "page:1"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "selector": "#a", "x": 1.0, "y": 2.0}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "x": 1.0}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "selector": "#a", "waitFor": "visible"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "selector": "#a", "action": "click"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "action": "fill"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "selector": "#a", "action": "fill", "value": 3}},
		{"surface_cdp", map[string]any{"surfaceId": "page:1", "method": "Runtime.evaluate", "params": map[string]any{"expression": "1"}}},
		{"surface_cdp", map[string]any{"surfaceId": "page:1", "method": "Surface.close"}},
		{"surface_cdp", map[string]any{"surfaceId": "page:1", "method": "AWCP.invoke"}},
		{"surface_cdp", map[string]any{"method": "DOM.getDocument"}},
		{"surface_cdp", map[string]any{"surfaceId": "page:1", "method": "DOM.getDocument", "requestId": "forged"}},
		{"surface_cdp", map[string]any{"surfaceId": "page:1", "method": "DOM.getDocument", "params": map[string]any{}, "paramsFile": "p.json"}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
		if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
			t.Errorf("%s %v: %#v %v", tc.tool, tc.args, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("rejected input reached the client: %#v", invoker.requests)
	}
}

func TestSurfaceEvaluateReadsExpressionFileUnderReadPolicy(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	root := execCtx.Session.WorkspaceRoot
	mustWriteFile(t, filepath.Join(root, "script.js"), "(() => document.title)()")
	mustWriteFile(t, filepath.Join(root, "empty.js"), "  \n")
	result, err := executor.Invoke(context.Background(), "surface_evaluate", map[string]any{"surfaceId": "page:1", "expressionFile": "script.js"}, execCtx)
	if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
		t.Fatalf("expressionFile: %#v %v", result, err)
	}
	params := invoker.requests[0].Payload["params"].(map[string]any)
	if params["expression"] != "(() => document.title)()" {
		t.Fatalf("expression: %#v", params)
	}
	if _, leaked := invoker.requests[0].Payload["expressionFile"]; leaked {
		t.Fatal("expressionFile leaked to the client")
	}

	result, err = executor.Invoke(context.Background(), "surface_evaluate", map[string]any{"surfaceId": "page:1", "expressionFile": "empty.js"}, execCtx)
	if err != nil || result.Error != "surface_evaluate_expression_file_invalid" {
		t.Fatalf("empty file: %#v %v", result, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.js")
	mustWriteFile(t, outside, "1")
	result, err = executor.Invoke(context.Background(), "surface_evaluate", map[string]any{"surfaceId": "page:1", "expressionFile": outside}, execCtx)
	if err != nil || result.Error != "surface_evaluate_expression_file_approval_required" || len(invoker.requests) != 1 {
		t.Fatalf("outside file must require read approval: %#v %v", result, err)
	}
}

func TestSurfaceScreenshotUsesPNGAndOptionalFullPage(t *testing.T) {
	for fullPage, want := range map[bool]map[string]any{
		false: {"format": "png"},
		true:  {"format": "png", "captureBeyondViewport": true},
	} {
		executor, execCtx, invoker := webControlTestRuntime(t, nil)
		args := map[string]any{"surfaceId": "page:1"}
		if fullPage {
			args["fullPage"] = true
		}
		// The scripted client returns no image; only the request shape is asserted.
		_, err := executor.Invoke(context.Background(), "surface_screenshot", args, execCtx)
		if err != nil || len(invoker.requests) != 1 {
			t.Fatalf("screenshot: %v %#v", err, invoker.requests)
		}
		request := invoker.requests[0]
		if request.Payload["method"] != desktopCdpCaptureScreenshotMethod || request.Payload["surfaceId"] != "page:1" || !reflect.DeepEqual(request.Payload["params"], want) {
			t.Fatalf("screenshot request: %#v", request.Payload)
		}
	}
}

func TestAwcpToolsUseTopLevelBusinessFields(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
		if request.Type == desktopAwcpInvokeAction {
			return map[string]any{"ok": true, "result": map[string]any{"itemId": "order-1", "containerId": "warehouse-7"}}
		}
		return nil
	})
	directory, err := executor.Invoke(context.Background(), "awcp_manual", map[string]any{"surfaceId": "page:1"}, execCtx)
	if err != nil || directory.ExitCode != 0 {
		t.Fatalf("directory: %#v %v", directory, err)
	}
	section, err := executor.Invoke(context.Background(), "awcp_manual", map[string]any{"surfaceId": "page:1", "section": "orders.read", "revision": "manual:1"}, execCtx)
	if err != nil || section.ExitCode != 0 {
		t.Fatalf("section: %#v %v", section, err)
	}
	invoked, err := executor.Invoke(context.Background(), "awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "manual:1", "action": "orders.read", "args": map[string]any{"id": 7.0}}, execCtx)
	if err != nil || invoked.ExitCode != 0 {
		t.Fatalf("invoke: %#v %v", invoked, err)
	}
	// Website result keys are page data and must not be filtered.
	if !strings.Contains(invoked.Output, `"itemId":"order-1"`) || !strings.Contains(invoked.Output, `"containerId":"warehouse-7"`) {
		t.Fatalf("page-owned result was altered: %s", invoked.Output)
	}
	if len(invoker.requests) != 3 {
		t.Fatalf("requests: %#v", invoker.requests)
	}
	if invoker.requests[0].Type != desktopAwcpManualAction || !reflect.DeepEqual(invoker.requests[0].Payload, map[string]any{"surfaceId": "page:1"}) {
		t.Fatalf("directory request: %#v", invoker.requests[0])
	}
	if !reflect.DeepEqual(invoker.requests[1].Payload, map[string]any{"surfaceId": "page:1", "section": "orders.read", "revision": "manual:1"}) {
		t.Fatalf("section request: %#v", invoker.requests[1])
	}
	if invoker.requests[2].Type != desktopAwcpInvokeAction || !reflect.DeepEqual(invoker.requests[2].Payload, map[string]any{"surfaceId": "page:1", "revision": "manual:1", "action": "orders.read", "args": map[string]any{"id": 7.0}}) {
		t.Fatalf("invoke request: %#v", invoker.requests[2])
	}
}

func TestAwcpToolsReportValidationInTheirOwnFieldNames(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"awcp_manual", map[string]any{"surfaceId": "page:1", "section": "orders.read"}},
		{"awcp_manual", map[string]any{"surfaceId": "page:1", "revision": "manual:1"}},
		{"awcp_manual", map[string]any{"surfaceId": "page:1", "section": "Not Valid", "revision": "manual:1"}},
		{"awcp_manual", map[string]any{"surfaceId": "page:1", "paramsFile": "p.json"}},
		{"awcp_invoke", map[string]any{"surfaceId": "page:1", "action": "orders.read", "args": map[string]any{}}},
		{"awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "manual:1", "args": map[string]any{}}},
		{"awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "manual:1", "action": "orders.read"}},
		{"awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "manual:1", "action": "orders.read", "args": "{}"}},
		{"awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "manual:1", "action": "orders.read", "args": map[string]any{}, "requestId": "forged"}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
		if err != nil || result.Error != "invalid_args" {
			t.Errorf("%s %v: %#v %v", tc.tool, tc.args, result, err)
			continue
		}
		if strings.Contains(result.Output, "params.") || strings.Contains(result.Output, `"path":["params"`) {
			t.Errorf("%s reports the wire envelope instead of its own fields: %s", tc.tool, result.Output)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("rejected AWCP input reached the client: %#v", invoker.requests)
	}
}

func TestWebControlToolSetMatchesConnectorRegistry(t *testing.T) {
	executor, execCtx, _ := webControlTestRuntime(t, nil)
	tools := (connector.Package{Manifest: connector.Manifest{ID: connector.WebControlConnectorID, Type: "native"}}).NativeTools()
	if len(tools) != 15 {
		t.Fatalf("web-control tools: %v", tools)
	}
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, def := range defs {
		defined[def.Name] = true
	}
	for _, tool := range tools {
		if !defined[tool] {
			t.Errorf("%s has no embedded definition", tool)
		}
		// Every registered tool has a handler: an unknown field is rejected by
		// the tool's own contract rather than by a missing dispatch entry.
		result, err := executor.Invoke(context.Background(), tool, map[string]any{"unknownField": true}, execCtx)
		if err != nil || result.Error != "invalid_args" {
			t.Errorf("%s dispatch: %#v %v", tool, result, err)
		}
	}
}

func TestSurfaceElementReportsNestedFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		evaluation map[string]any
		wantError  string
	}{
		{"missing element", map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"ok": false, "error": "element_not_found"}}}, "surface_element_failed"},
		{"script exception", map[string]any{"exceptionDetails": map[string]any{"text": "Invalid selector"}}, "desktop_cdp_evaluation_failed"},
		{"success", map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"ok": true}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, execCtx, invoker := webControlTestRuntime(t, func(ClientRequest) map[string]any {
				return map[string]any{"ok": true, "result": tc.evaluation}
			})
			result, err := executor.Invoke(context.Background(), "surface_element", map[string]any{"surfaceId": "page:1", "selector": "#name", "action": "focus"}, execCtx)
			if err != nil || result.Error != tc.wantError || (result.ExitCode != 0) != (tc.wantError != "") || len(invoker.requests) != 1 {
				t.Fatalf("result: %#v err: %v requests: %d", result, err, len(invoker.requests))
			}
			if tc.name == "missing element" && !strings.Contains(result.Output, "element_not_found") {
				t.Fatalf("lost business error: %s", result.Output)
			}
		})
	}
}

func TestSurfaceCloseUsesDiscoveredIdentityRegardlessOfURL(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
		if request.Payload["method"] == "Surface.list" {
			return map[string]any{"ok": true, "result": map[string]any{"surfaces": []any{
				map[string]any{"surfaceId": "page:1", "url": "https://example.com/"},
				map[string]any{"surfaceId": "page:2", "url": "https://example.com/"},
			}}}
		}
		return nil
	})
	listed, err := executor.Invoke(context.Background(), "surface_list", nil, execCtx)
	if err != nil || listed.ExitCode != 0 {
		t.Fatalf("list: %#v %v", listed, err)
	}
	response := listed.Structured["response"].(map[string]any)["result"].(map[string]any)
	id := response["surfaces"].([]any)[1].(map[string]any)["surfaceId"]
	closed, err := executor.Invoke(context.Background(), "surface_close", map[string]any{"surfaceId": id}, execCtx)
	if err != nil || closed.ExitCode != 0 || len(invoker.requests) != 2 {
		t.Fatalf("close: %#v %v requests: %#v", closed, err, invoker.requests)
	}
	if invoker.requests[1].Payload["method"] != "Surface.close" || invoker.requests[1].Payload["surfaceId"] != "page:2" {
		t.Fatalf("wrong target: %#v", invoker.requests[1])
	}
}
