package tools

import (
	"agent-platform/internal/toolinput"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

// builtin.web-control exposes typed, page-oriented tools. Each tool maps to an
// existing reverse request (a WorkPanel/webpage action, a CDP method or an AWCP
// call); the Desktop protocol itself is unchanged. The model only sees two
// identities: the url an item was opened with and a webpage's surfaceId.
// WorkPanel item identifiers and containers stay internal to Platform.

const (
	webControlWorkspaceAlias = "@workspace/"
	webControlChatAlias      = "@chat/"
)

// CDP methods without a dedicated tool. Methods that have one (navigation,
// screenshot, evaluate, click) are intentionally absent: there is one entry
// per operation.
var webControlRawCDPMethods = map[string]bool{
	"Page.enable":              true,
	"DOM.getDocument":          true,
	"DOM.querySelector":        true,
	"DOM.querySelectorAll":     true,
	"DOM.getOuterHTML":         true,
	"DOM.getBoxModel":          true,
	"Input.dispatchMouseEvent": true,
	"Input.dispatchKeyEvent":   true,
	"Input.insertText":         true,
	"Network.enable":           true,
	"Network.disable":          true,
}

var webControlElementActions = map[string]bool{"fill": true, "select": true, "focus": true, "scroll": true}

func isWebControlTool(name string) bool {
	id, ok := connector.NativeToolConnector(name)
	return ok && id == connector.WebControlConnectorID
}

func (t *RuntimeToolExecutor) invokeWebControl(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	switch toolName {
	case "workpanel_state":
		return t.webControlPanelState(ctx, args, execCtx)
	case "workpanel_open":
		return t.webControlPanelOpen(ctx, args, execCtx)
	case "workpanel_close":
		return t.webControlPanelClose(ctx, args, execCtx)
	case "surface_list":
		if failure, failed := webControlFields(args); failed {
			return failure, nil
		}
		return t.webControlSurfaceCall(ctx, "Surface.list", "", nil, execCtx)
	case "surface_state":
		return t.webControlSurfaceState(ctx, args, execCtx)
	case "surface_navigate":
		return t.webControlSurfaceNavigate(ctx, args, execCtx)
	case "surface_activate":
		return t.webControlSurfaceSimple(ctx, "Page.bringToFront", args, execCtx)
	case "surface_close":
		return t.webControlSurfaceSimple(ctx, "Surface.close", args, execCtx)
	case "surface_screenshot":
		return t.webControlSurfaceScreenshot(ctx, args, execCtx)
	case "surface_evaluate":
		return t.webControlSurfaceEvaluate(ctx, args, execCtx)
	case "surface_click":
		return t.webControlSurfaceClick(ctx, args, execCtx)
	case "surface_element":
		return t.webControlSurfaceElement(ctx, args, execCtx)
	case "surface_cdp":
		return t.webControlSurfaceCDP(ctx, args, execCtx)
	case "awcp_manual":
		return t.webControlAwcpManual(ctx, args, execCtx)
	case "awcp_invoke":
		return t.webControlAwcpInvoke(ctx, args, execCtx)
	default:
		return desktopActionErrorResult("unknown_tool", "unknown web-control tool", map[string]any{"tool": toolName}), nil
	}
}

func webControlInvalidArgs(message string, field string) ToolExecutionResult {
	details := map[string]any{"category": "validation", "stage": "arguments", "executionState": "not_started"}
	if field != "" {
		details["field"] = field
	}
	return desktopActionErrorResult("invalid_args", message, details)
}

// webControlFields rejects fields outside a tool's fixed input contract.
func webControlFields(args map[string]any, allowed ...string) (ToolExecutionResult, bool) {
	fields := map[string]bool{}
	for _, key := range allowed {
		fields[key] = true
	}
	for _, key := range toolinput.Keys(args) {
		if !fields[key] {
			return webControlInputError(toolinput.Unknown("", toolinput.Keys(fields))), true
		}
	}
	return ToolExecutionResult{}, false
}
func webControlInputError(err *toolinput.Error) ToolExecutionResult {
	details := err.Details()
	details["category"] = "validation"
	details["stage"] = "arguments"
	details["executionState"] = "not_started"
	return desktopActionErrorResult("invalid_args", err.Error(), details)
}

func webControlString(args map[string]any, field string, required bool) (string, ToolExecutionResult, bool) {
	raw, present := args[field]
	if !present {
		if required {
			return "", webControlInputError(toolinput.New(field, "non-empty JSON string", raw, false, `Provide a non-empty string, for example "text".`)), true
		}
		return "", ToolExecutionResult{}, false
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", webControlInputError(toolinput.New(field, "non-empty JSON string", raw, true, `Provide a non-empty string, for example "text".`)), true
	}
	return value, ToolExecutionResult{}, false
}

func webControlBool(args map[string]any, field string) (bool, bool, ToolExecutionResult, bool) {
	raw, present := args[field]
	if !present {
		return false, false, ToolExecutionResult{}, false
	}
	value, ok := raw.(bool)
	if !ok {
		return false, true, webControlInputError(toolinput.New(field, "JSON boolean", raw, true, "Use true or false without quotes.")), true
	}
	return value, true, ToolExecutionResult{}, false
}

func webControlSurfaceID(args map[string]any, required bool) (string, ToolExecutionResult, bool) {
	id, failure, failed := webControlString(args, "surfaceId", false)
	if failed {
		return "", failure, true
	}
	id = strings.TrimSpace(id)
	if id == "" && required {
		return "", webControlInvalidArgs("surfaceId is required; use the value returned by workpanel_open or surface_list", "surfaceId"), true
	}
	return id, ToolExecutionResult{}, false
}

// --- WorkPanel ---------------------------------------------------------------

func (t *RuntimeToolExecutor) webControlAction(ctx context.Context, action string, actionArgs map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if actionArgs == nil {
		actionArgs = map[string]any{}
	}
	return t.dispatchDesktopAction(ctx, action, map[string]any{"action": action, "args": actionArgs}, execCtx)
}

func (t *RuntimeToolExecutor) webControlPanelState(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args); failed {
		return failure, nil
	}
	result, err := t.webControlAction(ctx, "desktop.workpanel.getState", nil, execCtx)
	return projectWebControlPanelResult(result), err
}

type webControlTarget struct {
	web bool
	// url is the HTTP(S) address for webpages.
	url string
	// relativePath is the Workspace-relative file path for file previews.
	relativePath string
}

func resolveWebControlTarget(session QuerySession, raw string) (webControlTarget, ToolExecutionResult, bool) {
	value := strings.TrimSpace(raw)
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			return webControlTarget{}, webControlInvalidArgs("url must be a valid http:// or https:// address", "url"), true
		}
		if parsed.User != nil {
			return webControlTarget{}, webControlInvalidArgs("url must not contain credentials", "url"), true
		}
		return webControlTarget{web: true, url: value}, ToolExecutionResult{}, false
	case strings.HasPrefix(lower, webControlWorkspaceAlias), strings.HasPrefix(lower, webControlChatAlias):
		_, suffix, _ := strings.Cut(strings.ReplaceAll(value, "\\", "/"), "/")
		if strings.Trim(suffix, "/") == "" {
			return webControlTarget{}, webControlInvalidArgs("url must name a file, for example @workspace/report.html", "url"), true
		}
		relative, err := resolveDesktopActionAlias(session, value)
		if err != nil {
			return webControlTarget{}, desktopActionErrorResult("invalid_args", err.Error(), map[string]any{
				"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "url",
				"recovery": map[string]any{"strategy": "fix_input", "message": "Use @workspace/<path> for the bound project or @chat/<path> for the current Chat. The file must be inside the current trusted Workspace; parent traversal is not allowed."},
			}), true
		}
		return webControlTarget{relativePath: relative}, ToolExecutionResult{}, false
	default:
		return webControlTarget{}, desktopActionErrorResult("invalid_args", "url must start with http://, https://, @workspace/ or @chat/", map[string]any{
			"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "url",
			"recovery": map[string]any{"strategy": "fix_input", "message": "Write webpages with an explicit scheme (http://localhost:3000) and files with an explicit root (@workspace/report.html). Bare paths, host names without a scheme, absolute paths and file:// are not accepted."},
		}), true
	}
}

func (t *RuntimeToolExecutor) webControlPanelOpen(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "url", "title", "reload"); failed {
		return failure, nil
	}
	raw, failure, failed := webControlString(args, "url", true)
	if failed {
		return failure, nil
	}
	title, failure, failed := webControlString(args, "title", false)
	if failed {
		return failure, nil
	}
	reload, _, failure, failed := webControlBool(args, "reload")
	if failed {
		return failure, nil
	}
	if execCtx == nil {
		return desktopActionErrorResult("invalid_execution_context", "run execution context is required", nil), nil
	}
	target, failure, failed := resolveWebControlTarget(execCtx.Session, raw)
	if failed {
		return failure, nil
	}
	if !target.web {
		// Reopening a file preview activates the existing item and reloads it,
		// so reload needs no separate request.
		fileArgs := map[string]any{"path": target.relativePath}
		if title != "" {
			fileArgs["title"] = title
		}
		result, err := t.webControlAction(ctx, "desktop.workpanel.openLocalFile", fileArgs, execCtx)
		return projectWebControlPanelResult(result), err
	}
	if title != "" {
		return webControlInvalidArgs("title applies to file previews only; a webpage shows its own title", "title"), nil
	}
	opened, err := t.webControlAction(ctx, "desktop.workpanel.openWeb", map[string]any{"url": target.url}, execCtx)
	if err != nil || opened.ExitCode != 0 || !reload {
		return projectWebControlPanelResult(opened), err
	}
	refreshed, err := t.webControlAction(ctx, "desktop.workpanel.refreshWeb", map[string]any{"url": target.url}, execCtx)
	if err != nil || refreshed.ExitCode != 0 {
		return projectWebControlPanelResult(refreshed), err
	}
	return projectWebControlPanelResult(opened), nil
}

func (t *RuntimeToolExecutor) webControlPanelClose(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "url", "all"); failed {
		return failure, nil
	}
	all, _, failure, failed := webControlBool(args, "all")
	if failed {
		return failure, nil
	}
	_, hasURL := args["url"]
	if all == hasURL {
		return webControlInvalidArgs("provide exactly one of url (close one item) or all: true (close the whole WorkPanel)", ""), nil
	}
	if all {
		result, err := t.webControlAction(ctx, "desktop.workpanel.closeWorkpanel", nil, execCtx)
		return projectWebControlPanelResult(result), err
	}
	raw, failure, failed := webControlString(args, "url", true)
	if failed {
		return failure, nil
	}
	if execCtx == nil {
		return desktopActionErrorResult("invalid_execution_context", "run execution context is required", nil), nil
	}
	target, failure, failed := resolveWebControlTarget(execCtx.Session, raw)
	if failed {
		return failure, nil
	}
	if target.web {
		return webControlInvalidArgs("close webpages with surface_close using a surfaceId from surface_list or workpanel_open; url is only for file previews", "url"), nil
	}
	state, err := t.webControlAction(ctx, "desktop.workpanel.getState", nil, execCtx)
	if err != nil || state.ExitCode != 0 {
		return projectWebControlPanelResult(state), err
	}
	itemID, problem := findWebControlPanelItem(webControlPanelItems(state.Structured), target)
	if itemID == "" {
		view := projectWebControlPanelResult(state)
		return desktopActionErrorResult("workpanel_item_not_found", problem, map[string]any{
			"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "url",
			"workpanel": view.Structured,
		}), nil
	}
	result, err := t.webControlAction(ctx, "desktop.workpanel.closeTab", map[string]any{"tabId": itemID}, execCtx)
	return projectWebControlPanelResult(result), err
}

// webControlPanelItems reads the raw items from a getState result.
func webControlPanelItems(structured map[string]any) []any {
	response, _ := structured["response"].(map[string]any)
	result, _ := response["result"].(map[string]any)
	state, _ := result["state"].(map[string]any)
	items, _ := state["items"].([]any)
	return items
}

func findWebControlPanelItem(items []any, target webControlTarget) (string, string) {
	var matched string
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		descriptor, _ := item["descriptor"].(map[string]any)
		itemID, _ := item["itemId"].(string)
		relative, _ := descriptor["workspaceRelativePath"].(string)
		if itemID == "" || target.web || descriptor["kind"] != "local-file" || relative == "" || relative != target.relativePath {
			continue
		}
		if matched != "" {
			return "", "several open file previews match this Workspace path; no item was closed"
		}
		matched = itemID
	}
	if matched != "" {
		return matched, ""
	}
	return "", "no file preview has a recorded Workspace path matching url; call workpanel_state; file names alone cannot identify a preview"
}

// projectWebControlPanelResult replaces raw WorkPanel workspaces with the
// model-facing view and removes item/container identifiers.
func projectWebControlPanelResult(result ToolExecutionResult) ToolExecutionResult {
	if result.Structured == nil {
		return result
	}
	projected, _ := projectWebControlPanelValue(result.Structured).(map[string]any)
	return webControlRestructured(result, projected)
}

func webControlRestructured(result ToolExecutionResult, structured map[string]any) ToolExecutionResult {
	data, err := json.Marshal(structured)
	if err != nil {
		return result
	}
	result.Structured = structured
	result.Output = string(data)
	return result
}

var webControlPanelHiddenKeys = map[string]bool{
	"containerId": true, "workspaceId": true, "ownerChatId": true, "closedItemId": true,
	"itemId": true, "stableKey": true, "activeItemId": true,
}

func projectWebControlPanelValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if items, ok := typed["items"].([]any); ok {
			if _, isWorkspace := typed["activeItemId"]; isWorkspace {
				active, _ := typed["activeItemId"].(string)
				view := make([]any, 0, len(items))
				for _, raw := range items {
					if item, ok := raw.(map[string]any); ok {
						view = append(view, projectWebControlPanelItem(item, active))
					}
				}
				return map[string]any{"items": view}
			}
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if webControlPanelHiddenKeys[key] {
				continue
			}
			out[key] = projectWebControlPanelValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = projectWebControlPanelValue(item)
		}
		return out
	default:
		return value
	}
}

func projectWebControlPanelItem(item map[string]any, activeItemID string) map[string]any {
	descriptor, _ := item["descriptor"].(map[string]any)
	out := map[string]any{}
	switch kind, _ := descriptor["kind"].(string); kind {
	case "web":
		out["kind"] = "web"
		out["url"] = descriptor["url"]
	case "local-file":
		out["kind"] = "file"
		if relative, _ := descriptor["workspaceRelativePath"].(string); relative != "" {
			out["url"] = webControlWorkspaceAlias + relative
		}
		if name, _ := descriptor["fileName"].(string); name != "" {
			out["fileName"] = name
		}
	default:
		out["kind"] = kind
	}
	for _, key := range []string{"title", "closable", "pinned"} {
		if value, ok := item[key]; ok {
			out[key] = value
		}
	}
	itemID, _ := item["itemId"].(string)
	out["active"] = itemID != "" && itemID == activeItemID
	return out
}

// --- Surface -----------------------------------------------------------------

func (t *RuntimeToolExecutor) webControlSurfaceCall(ctx context.Context, method string, surfaceID string, params map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	request := map[string]any{"method": method}
	if surfaceID != "" {
		request["surfaceId"] = surfaceID
	}
	if params != nil {
		request["params"] = params
	}
	result, err := t.invokeDesktopCDP(ctx, request, execCtx)
	return stripWebControlContainers(result), err
}

// stripWebControlContainers hides container identity from Desktop-shaped
// lifecycle results. It is not applied to page-owned data (script values,
// DOM, AWCP results), whose keys belong to the website.
func stripWebControlContainers(result ToolExecutionResult) ToolExecutionResult {
	if result.Structured == nil {
		return result
	}
	stripped, _ := stripWebControlKey(result.Structured, "containerId").(map[string]any)
	return webControlRestructured(result, stripped)
}

func stripWebControlKey(value any, hidden string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key != hidden {
				out[key] = stripWebControlKey(item, hidden)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = stripWebControlKey(item, hidden)
		}
		return out
	default:
		return value
	}
}

func (t *RuntimeToolExecutor) webControlSurfaceState(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, false)
	if failed {
		return failure, nil
	}
	if surfaceID == "" {
		return t.webControlSurfaceCall(ctx, "Surface.getCurrent", "", nil, execCtx)
	}
	return t.webControlSurfaceCall(ctx, "Surface.getState", surfaceID, nil, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceSimple(ctx context.Context, method string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	return t.webControlSurfaceCall(ctx, method, surfaceID, nil, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceNavigate(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "action", "url", "ignoreCache"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	action, failure, failed := webControlString(args, "action", true)
	if failed {
		return failure, nil
	}
	_, hasURL := args["url"]
	ignoreCache, hasIgnoreCache, failure, failed := webControlBool(args, "ignoreCache")
	if failed {
		return failure, nil
	}
	if hasURL && action != "goto" {
		return webControlInvalidArgs("url is only valid with action: goto", "url"), nil
	}
	if hasIgnoreCache && action != "reload" {
		return webControlInvalidArgs("ignoreCache is only valid with action: reload", "ignoreCache"), nil
	}
	switch action {
	case "goto":
		target, failure, failed := webControlString(args, "url", true)
		if failed {
			return failure, nil
		}
		lower := strings.ToLower(strings.TrimSpace(target))
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return webControlInvalidArgs("url must start with http:// or https://", "url"), nil
		}
		return t.webControlSurfaceCall(ctx, "Page.navigate", surfaceID, map[string]any{"url": strings.TrimSpace(target)}, execCtx)
	case "reload":
		var params map[string]any
		if hasIgnoreCache {
			params = map[string]any{"ignoreCache": ignoreCache}
		}
		return t.webControlSurfaceCall(ctx, "Page.reload", surfaceID, params, execCtx)
	case "back":
		return t.webControlSurfaceCall(ctx, "Surface.goBack", surfaceID, nil, execCtx)
	default:
		return webControlInvalidArgs("action must be goto, reload or back", "action"), nil
	}
}

func (t *RuntimeToolExecutor) webControlSurfaceScreenshot(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "fullPage"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	fullPage, _, failure, failed := webControlBool(args, "fullPage")
	if failed {
		return failure, nil
	}
	params := map[string]any{"format": "png"}
	if fullPage {
		params["captureBeyondViewport"] = true
	}
	return t.invokeDesktopCDP(ctx, map[string]any{"method": desktopCdpCaptureScreenshotMethod, "surfaceId": surfaceID, "params": params}, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceEvaluate(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "expression", "expressionFile", "awaitPromise"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	_, hasExpression := args["expression"]
	_, hasFile := args["expressionFile"]
	if hasExpression == hasFile {
		return webControlInvalidArgs("provide exactly one of expression or expressionFile", ""), nil
	}
	awaitPromise, hasAwait, failure, failed := webControlBool(args, "awaitPromise")
	if failed {
		return failure, nil
	}
	if !hasAwait {
		awaitPromise = true
	}
	var expression string
	if hasExpression {
		expression, failure, failed = webControlString(args, "expression", true)
		if failed {
			return failure, nil
		}
	} else {
		file, failure, failed := webControlString(args, "expressionFile", true)
		if failed {
			return failure, nil
		}
		data, failure, failed := t.readDesktopInputFile("expressionFile", "surface_evaluate_expression_file", file, execCtx)
		if failed {
			return failure, nil
		}
		if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
			return desktopActionErrorResult("surface_evaluate_expression_file_invalid", "expressionFile must be a non-empty UTF-8 JavaScript file", nil), nil
		}
		expression = string(data)
	}
	params := map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": awaitPromise}
	return t.invokeDesktopCDP(ctx, map[string]any{"method": "Runtime.evaluate", "surfaceId": surfaceID, "params": params}, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceClick(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "selector", "x", "y", "waitFor"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	_, hasSelector := args["selector"]
	_, hasX := args["x"]
	_, hasY := args["y"]
	if hasSelector == (hasX || hasY) || hasX != hasY {
		return webControlInvalidArgs("provide either selector or both x and y", ""), nil
	}
	params := map[string]any{}
	if hasSelector {
		selector, failure, failed := webControlString(args, "selector", true)
		if failed {
			return failure, nil
		}
		params["selector"] = selector
	} else {
		// Types are preserved so Desktop reports the exact invalid coordinate.
		params["x"], params["y"] = args["x"], args["y"]
	}
	if waitFor, present := args["waitFor"]; present {
		if _, ok := waitFor.(map[string]any); !ok {
			return webControlInvalidArgs("waitFor must be an object", "waitFor"), nil
		}
		params["waitFor"] = waitFor
	}
	return t.invokeDesktopCDP(ctx, map[string]any{"method": "Input.click", "surfaceId": surfaceID, "params": params}, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceElement(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "selector", "action", "value"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	selector, failure, failed := webControlString(args, "selector", true)
	if failed {
		return failure, nil
	}
	action, failure, failed := webControlString(args, "action", true)
	if failed {
		return failure, nil
	}
	if !webControlElementActions[action] {
		return webControlInvalidArgs("action must be fill, select, focus or scroll; use surface_click to click", "action"), nil
	}
	actionArgs := map[string]any{"surfaceId": surfaceID, "selector": selector, "action": action}
	if raw, present := args["value"]; present {
		value, ok := raw.(string)
		if !ok {
			return webControlInvalidArgs("value must be a string", "value"), nil
		}
		actionArgs["value"] = value
	}
	result, err := t.webControlAction(ctx, "desktop.web.interactElement", actionArgs, execCtx)
	if err != nil || result.ExitCode != 0 {
		return result, err
	}
	// This action wraps a fixed Runtime.evaluate script. Its CDP exception
	// and script result are distinct from the successful transport envelope.
	response, _ := result.Structured["response"].(map[string]any)
	evaluation, _ := response["result"].(map[string]any)
	if failure, failed := desktopCDPEvaluationFailure(map[string]any{
		"method": "Runtime.evaluate", "result": evaluation,
	}, result.Structured); failed {
		return failure, nil
	}
	remote, _ := evaluation["result"].(map[string]any)
	value, _ := remote["value"].(map[string]any)
	if value["ok"] == false {
		code, _ := value["error"].(string)
		result.Structured["ok"] = false
		result.Structured["error"] = map[string]any{
			"code":    "surface_element_failed",
			"message": firstDesktopActionMessage(code, "element operation failed"),
		}
		result = webControlRestructured(result, result.Structured)
		result.ExitCode = -1
		result.Error = "surface_element_failed"
	}
	return result, nil
}

func (t *RuntimeToolExecutor) webControlSurfaceCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "method", "params", "paramsFile"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	method, failure, failed := webControlString(args, "method", true)
	if failed {
		return failure, nil
	}
	method = strings.TrimSpace(method)
	if !webControlRawCDPMethods[method] {
		return desktopActionErrorResult("invalid_args", "method is not available through surface_cdp", map[string]any{
			"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "method",
			"recovery": map[string]any{"strategy": "fix_input", "message": "Navigation, screenshots, script evaluation and clicks have dedicated surface_* tools; AWCP uses awcp_manual and awcp_invoke."},
		}), nil
	}
	request := map[string]any{"method": method, "surfaceId": surfaceID}
	for _, key := range []string{"params", "paramsFile"} {
		if value, present := args[key]; present {
			request[key] = value
		}
	}
	return t.invokeDesktopCDP(ctx, request, execCtx)
}

// --- AWCP --------------------------------------------------------------------

func (t *RuntimeToolExecutor) webControlAwcpManual(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "section", "revision"); failed {
		return failure, nil
	}
	_, hasSection := args["section"]
	_, hasRevision := args["revision"]
	if hasSection != hasRevision {
		return webControlInvalidArgs("section and revision must be provided together; omit both to read the directory", ""), nil
	}
	request := map[string]any{"method": desktopAwcpGetManualMethod}
	if value, present := args["surfaceId"]; present {
		request["surfaceId"] = value
	}
	if hasSection {
		request["params"] = map[string]any{"section": args["section"], "revision": args["revision"]}
	}
	result, err := t.invokeDesktopCDP(ctx, request, execCtx)
	return flattenWebControlAwcpError(result, "awcp_manual"), err
}

func (t *RuntimeToolExecutor) webControlAwcpInvoke(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "revision", "action", "args", "paramsFile"); failed {
		return failure, nil
	}
	params := map[string]any{}
	for _, key := range []string{"revision", "action", "args"} {
		if value, present := args[key]; present {
			params[key] = value
		}
	}
	request := map[string]any{"method": desktopAwcpInvokeMethod, "params": params}
	if file, present := args["paramsFile"]; present {
		request["paramsFile"] = file
		if len(params) == 0 {
			delete(request, "params")
		}
	}

	if value, present := args["surfaceId"]; present {
		request["surfaceId"] = value
	}
	result, err := t.invokeDesktopCDP(ctx, request, execCtx)
	return flattenWebControlAwcpError(result, "awcp_invoke"), err
}

// The shared AWCP validator describes the wire envelope, where the business
// fields live under params. The typed tools take them at the top level, so
// Platform-side validation errors are reported in the tool's own terms.
func flattenWebControlAwcpError(result ToolExecutionResult, toolName string) ToolExecutionResult {
	if result.Error != "invalid_args" || result.Structured == nil {
		return result
	}
	structured := cloneDesktopMap(result.Structured)
	if errorNode := cloneDesktopMapValue(structured["error"]); len(errorNode) > 0 {
		if message, ok := errorNode["message"].(string); ok {
			message = strings.ReplaceAll(message, "AWCP.invoke params", toolName+" input")
			message = strings.ReplaceAll(message, "AWCP.getManual params", toolName+" input")
			errorNode["message"] = strings.ReplaceAll(message, "params.", "")
		}
		structured["error"] = errorNode
	}
	if details := cloneDesktopMapValue(structured["details"]); len(details) > 0 {
		if fieldPath, ok := details["path"].([]string); ok && len(fieldPath) > 0 && fieldPath[0] == "params" {
			details["path"] = fieldPath[1:]
		}
		structured["details"] = details
	}
	return webControlRestructured(result, structured)
}
