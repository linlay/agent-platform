package tools

import (
	"agent-platform/internal/toolinput"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// builtin.web-control exposes typed, page-oriented tools. Each tool maps to an
// existing reverse request (a WorkPanel/webpage action, a CDP method or an AWCP
// call). The model sees the url an item was opened with and one surfaceId per
// WorkPanel tab, with the capabilities Desktop reports for it. Webpages and
// HTML file previews are live page surfaces; every other tab has an item
// surfaceId that can only be activated or closed. Containers stay internal.

const (
	webControlWorkspaceAlias = "@workspace/"
	webControlChatAlias      = "@chat/"

	webControlWorkspaceRoot = "workspace"
	webControlChatRoot      = "chat"

	// Desktop prefixes the surfaceId of a tab that is not a live page.
	webControlItemSurfacePrefix = "workpanel-item:"
)

// An HTML preview registers its page shortly after the tab opens.
var (
	webControlSurfaceWait = 8 * time.Second
	webControlSurfacePoll = 250 * time.Millisecond
)

var webControlElementActions = map[string]bool{
	"fill": true, "select": true, "focus": true, "scroll": true,
	"hover": true, "type": true, "press": true, "check": true, "uncheck": true,
}

var webControlPressKeys = map[string]bool{
	"Enter": true, "Escape": true, "Tab": true, "Shift+Tab": true,
	"ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true,
	"Home": true, "End": true, "PageUp": true, "PageDown": true,
	"Backspace": true, "Delete": true, "Space": true, "ControlOrMeta+A": true,
}

func isWebControlTool(name string) bool {
	id, ok := connector.NativeToolConnector(name)
	return ok && id == connector.WebControlConnectorID
}

// A Run may stay suspended this long while the user connects the browser it needs.
const (
	webControlConnectionWaitLimit = 5 * time.Minute
	webControlConnectionPollMs    = 20000
	webControlConnectionPollSlack = 10 * time.Second
)

// The router does not time web-control calls; each Desktop request is bounded
// here so that waiting for a browser connection is not cut short.
func (t *RuntimeToolExecutor) invokeWebControl(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	result, err := t.invokeWebControlTimed(ctx, toolName, args, execCtx)
	if err != nil || result.ExitCode == 0 || result.Error != "browser_connection_required" {
		return result, err
	}
	// Desktop raises browser_connection_required before dispatching any page work,
	// so the same call runs once more after the user connects. Desktop decides
	// whether this Run may wait at all.
	if !t.awaitWebControlConnection(ctx, execCtx) {
		return result, nil
	}
	return t.invokeWebControlTimed(ctx, toolName, args, execCtx)
}

func (t *RuntimeToolExecutor) invokeWebControlTimed(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, toolCallTimeout(execCtx))
	defer cancel()
	result, err := t.invokeWebControlTool(callCtx, toolName, args, execCtx)
	return webControlBrowserConnectionResult(result), err
}

// awaitWebControlConnection polls Desktop in bounded steps until the Run's
// browser is connected, Desktop reports that this Run cannot wait, the Run is
// cancelled or the overall limit passes.
func (t *RuntimeToolExecutor) awaitWebControlConnection(ctx context.Context, execCtx *ExecutionContext) bool {
	deadline := time.Now().Add(webControlConnectionWaitLimit)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, time.Duration(webControlConnectionPollMs)*time.Millisecond+webControlConnectionPollSlack)
		result, err := t.webControlSurfaceCall(pollCtx, "Browser.awaitConnection", "", map[string]any{"timeoutMs": webControlConnectionPollMs}, execCtx)
		cancel()
		if err != nil || result.ExitCode != 0 {
			return false
		}
		response, _ := result.Structured["response"].(map[string]any)
		status, _ := response["result"].(map[string]any)
		switch status["status"] {
		case "connected":
			return true
		case "pending":
			continue
		default:
			return false
		}
	}
	return false
}

func (t *RuntimeToolExecutor) invokeWebControlTool(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	if id, _ := args["surfaceId"].(string); strings.HasPrefix(strings.TrimSpace(id), webControlItemSurfacePrefix) &&
		toolName != "surface_activate" && toolName != "surface_close" {
		return desktopActionErrorResult("capability_denied", "this WorkPanel tab is not a live page; it can only be activated or closed", map[string]any{
			"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "surfaceId",
			"capabilities": []string{"activate", "close"},
		}), nil
	}
	switch toolName {
	case "surface_browser":
		return t.webControlSurfaceBrowser(ctx, args, execCtx)
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
	case "surface_open":
		return t.webControlSurfaceOpen(ctx, args, execCtx)
	case "surface_observe":
		return t.webControlSurfaceObserve(ctx, args, execCtx)
	case "surface_navigate":
		return t.webControlSurfaceNavigate(ctx, args, execCtx)
	case "surface_activate":
		return t.webControlSurfaceSimple(ctx, "Page.bringToFront", args, execCtx)
	case "surface_close":
		return t.webControlSurfaceSimple(ctx, "Surface.close", args, execCtx)
	case "surface_viewport":
		return t.webControlSurfaceViewport(ctx, args, execCtx)
	case "surface_screenshot":
		return t.webControlSurfaceScreenshot(ctx, args, execCtx)
	case "surface_click":
		return t.webControlSurfaceClick(ctx, args, execCtx)
	case "surface_element":
		return t.webControlSurfaceElement(ctx, args, execCtx)
	case "surface_scroll":
		return t.webControlSurfaceScroll(ctx, args, execCtx)
	case "surface_dialog":
		return t.webControlSurfaceDialog(ctx, args, execCtx)
	case "surface_upload":
		return t.webControlSurfaceUpload(ctx, args, execCtx)
	case "surface_downloads":
		return t.webControlSurfaceDownloads(ctx, args, execCtx)
	case "surface_login":
		return t.webControlSurfaceLogin(ctx, args, execCtx)
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
		return false, true, webControlInputError(toolinput.New(field, "JSON boolean", raw, true, "Provide a JSON boolean.")), true
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
	return projectWebControlPanelStateResult(result, t.cfg.RuntimeMode == config.RuntimeModeDesktop), err
}

type webControlTarget struct {
	web bool
	// url is the HTTP(S) address for webpages.
	url string
	// root names the tree a file preview is read from: the bound project
	// Workspace or the current Chat directory.
	root string
	// relativePath is the file path relative to root.
	relativePath string
}

func (target webControlTarget) fileURL() string {
	if target.root == webControlChatRoot {
		return webControlChatAlias + target.relativePath
	}
	return webControlWorkspaceAlias + target.relativePath
}

// resolveWebControlChatFile returns the Chat-relative path of an @chat/ file.
// The Chat directory is its own root: it does not need a project Workspace.
func resolveWebControlChatFile(session QuerySession, raw string) (string, error) {
	chatDir := accesspolicy.SessionChatDir(session)
	if chatDir == "" {
		return "", fmt.Errorf("the current Chat directory is unavailable")
	}
	_, candidate, err := resolveDesktopActionAliasTarget(session, raw)
	if err != nil {
		return "", err
	}
	root, err := pathutil.Canonicalize(chatDir)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root.Host, candidate.Host)
	if err != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("target is outside the current Chat")
	}
	// Reuses the Chat resource rules, so tool-internal files stay private.
	reference, err := chat.BuildChatAliasRef(filepath.ToSlash(relative))
	if err != nil {
		return "", fmt.Errorf("this Chat path cannot be previewed")
	}
	if info, statErr := os.Stat(candidate.Host); statErr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("file_unavailable: %s is missing or is not a regular file", reference)
	}
	return strings.TrimPrefix(reference, chat.ChatScopeAlias), nil
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
		target := webControlTarget{root: webControlWorkspaceRoot}
		var err error
		if strings.HasPrefix(lower, webControlChatAlias) {
			target.root = webControlChatRoot
			target.relativePath, err = resolveWebControlChatFile(session, value)
		} else {
			target.relativePath, err = resolveDesktopActionAlias(session, value)
		}
		if err != nil {
			return webControlTarget{}, desktopActionErrorResult("invalid_args", err.Error(), map[string]any{
				"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "url",
				"recovery": map[string]any{"strategy": "fix_input", "message": "Use @workspace/<path> for a file in the bound project and @chat/<path> for a file in the current Chat, such as a published artifact url. Each path must stay inside its own root; parent traversal is not allowed."},
			}), true
		}
		return target, ToolExecutionResult{}, false
	default:
		return webControlTarget{}, desktopActionErrorResult("invalid_args", "url must start with http://, https://, @workspace/ or @chat/", map[string]any{
			"category": "validation", "stage": "arguments", "executionState": "not_started", "field": "url",
			"recovery": map[string]any{"strategy": "fix_input", "message": "Write webpages with an explicit scheme (http://localhost:3000) and files with an explicit root (@workspace/report.html, or @chat/artifacts/... for a published artifact). Bare paths, host names without a scheme, absolute paths, @runtime/ and file:// are not accepted."},
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
		fileArgs := map[string]any{"root": target.root, "path": target.relativePath}
		if artifactID := t.webControlPublishedArtifactID(execCtx.Session.ChatID, target); artifactID != "" {
			fileArgs["artifactId"] = artifactID
		}
		if title != "" {
			fileArgs["title"] = title
		}
		result, err := t.webControlAction(ctx, "desktop.workpanel.openLocalFile", fileArgs, execCtx)
		if err != nil || result.ExitCode != 0 {
			return projectWebControlPanelResult(result), err
		}
		return t.webControlOpenedFile(ctx, result, target, execCtx), nil
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

// webControlPanelClose closes the whole WorkPanel. One tab, page or not, is
// closed with surface_close and its surfaceId.
func (t *RuntimeToolExecutor) webControlPanelClose(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "all"); failed {
		return failure, nil
	}
	all, _, failure, failed := webControlBool(args, "all")
	if failed {
		return failure, nil
	}
	if !all {
		return webControlInvalidArgs("workpanel_close requires all: true; close one tab with surface_close and its surfaceId", "all"), nil
	}
	result, err := t.webControlAction(ctx, "desktop.workpanel.closeWorkpanel", nil, execCtx)
	return projectWebControlPanelResult(result), err
}

// webControlPublishedArtifactID returns the manifest identity of a published
// Chat file. Desktop needs the real id, not only the path: saving an edited
// artifact is checked against the manifest entry.
func (t *RuntimeToolExecutor) webControlPublishedArtifactID(chatID string, target webControlTarget) string {
	if target.root != webControlChatRoot || !strings.HasPrefix(target.relativePath, "artifacts/") {
		return ""
	}
	reader, ok := t.chats.(interface {
		PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
	})
	if !ok {
		return ""
	}
	want, err := chat.BuildChatScopeRef(target.relativePath)
	if err != nil {
		return ""
	}
	items, err := reader.PublishedArtifacts(chatID)
	if err != nil {
		return ""
	}
	// Republishing a file appends an entry for the same path; the last one is current.
	artifactID := ""
	for _, item := range items {
		if chat.BareChatScopeRef(item.URL) == want {
			artifactID = item.ArtifactID
		}
	}
	return artifactID
}

// webControlPanelItems reads the raw items of the workspace in a WorkPanel
// result: getState and Workspace files report it as state, Chat files as
// workspace.
func webControlPanelItems(structured map[string]any) []any {
	response, _ := structured["response"].(map[string]any)
	result, _ := response["result"].(map[string]any)
	for _, key := range []string{"state", "workspace"} {
		if workspace, ok := result[key].(map[string]any); ok {
			items, _ := workspace["items"].([]any)
			return items
		}
	}
	return nil
}

// findWebControlPanelFileItem returns the one open preview recorded for the file.
func findWebControlPanelFileItem(items []any, target webControlTarget) map[string]any {
	var matched map[string]any
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		descriptor, _ := item["descriptor"].(map[string]any)
		root, relative := webControlPanelFile(descriptor)
		if target.web || root == "" || relative == "" || root != target.root || relative != target.relativePath {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = item
	}
	return matched
}

// webControlOpenedFile reports the surfaceId and capabilities of the file tab
// just opened. An HTML preview registers its page after the tab appears, so a
// missing page surfaceId is read again until the preview is ready.
func (t *RuntimeToolExecutor) webControlOpenedFile(ctx context.Context, opened ToolExecutionResult, target webControlTarget, execCtx *ExecutionContext) ToolExecutionResult {
	item := findWebControlPanelFileItem(webControlPanelItems(opened.Structured), target)
	deadline := time.Now().Add(webControlSurfaceWait)
wait:
	for item != nil && item["surfaceId"] == nil && webControlCapabilitiesInclude(item["capabilities"], "script") && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			break wait
		case <-time.After(webControlSurfacePoll):
		}
		state, err := t.webControlAction(ctx, "desktop.workpanel.getState", nil, execCtx)
		if err != nil || state.ExitCode != 0 {
			break
		}
		item = findWebControlPanelFileItem(webControlPanelItems(state.Structured), target)
	}
	projected := projectWebControlPanelResult(opened)
	response, _ := projected.Structured["response"].(map[string]any)
	result, _ := response["result"].(map[string]any)
	if item == nil || result == nil {
		return projected
	}
	for _, key := range []string{"surfaceId", "capabilities"} {
		if value, ok := item[key]; ok {
			result[key] = value
		}
	}
	return webControlRestructured(projected, projected.Structured)
}

func webControlCapabilitiesInclude(value any, capability string) bool {
	list, _ := value.([]any)
	for _, entry := range list {
		if entry == capability {
			return true
		}
	}
	return false
}

// webControlPanelFile reads the root and root-relative path a WorkPanel item
// previews. A Workspace file is a local preview. A Chat file is a native
// HTML/image preview or a WebClient resource page; each records its
// Chat-relative path under the artifact or reference profile.
func webControlPanelFile(descriptor map[string]any) (string, string) {
	context, _ := descriptor["context"].(map[string]any)
	switch kind, _ := descriptor["kind"].(string); kind {
	case "local-file":
		relative, _ := descriptor["workspaceRelativePath"].(string)
		return webControlWorkspaceRoot, relative
	case "webclient":
		module, _ := descriptor["module"].(string)
		relative, _ := context["relativePath"].(string)
		return webControlProfileRoot(module), relative
	case "native":
		if surface, _ := descriptor["surfaceKey"].(string); surface == "document-html" {
			profile, _ := context["sourceKind"].(string)
			display, _ := context["displayUrl"].(string)
			_, relative, _ := strings.Cut(display, ":///")
			return webControlProfileRoot(profile), relative
		}
		profile, _ := context["profile"].(string)
		relative, _ := context["relativePath"].(string)
		return webControlProfileRoot(profile), relative
	}
	return "", ""
}

func webControlProfileRoot(profile string) string {
	switch profile {
	case "artifact", "reference":
		return webControlChatRoot
	case "workspace-file":
		return webControlWorkspaceRoot
	}
	return ""
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
	"itemId": true, "stableKey": true, "activeItemId": true, "activeFile": true,
}

// Only workpanel_state in the Desktop runtime returns original-file metadata.
// Desktop Main resolves it from the trusted Run's current active item, never
// from renderer path data. Generic panel results and Standalone replies keep
// this extension hidden, even when a caller includes a similarly named field.
func projectWebControlPanelStateResult(result ToolExecutionResult, allowActiveFile bool) ToolExecutionResult {
	projected := projectWebControlPanelResult(result)
	if !allowActiveFile || result.ExitCode != 0 {
		return projected
	}
	response, _ := result.Structured["response"].(map[string]any)
	actionResult, _ := response["result"].(map[string]any)
	activeFile, present := actionResult["activeFile"]
	projectedResponse, _ := projected.Structured["response"].(map[string]any)
	projectedActionResult, _ := projectedResponse["result"].(map[string]any)
	if !present || projectedActionResult == nil {
		return desktopActionErrorResult("desktop_workpanel_contract_mismatch",
			"Desktop WorkPanel state reply must include activeFile. Update Desktop and Agent Platform together.",
			map[string]any{"category": "validation", "stage": "desktop_response", "requiredField": "activeFile"})
	}
	projectedActionResult["activeFile"] = projectWebControlActiveFile(activeFile)
	return webControlRestructured(projected, projected.Structured)
}

func projectWebControlActiveFile(value any) any {
	file, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	name, nameOK := file["fileName"].(string)
	kind, kindOK := file["kind"].(string)
	filePath, pathOK := file["path"].(string)
	mimeType, mimeOK := file["mimeType"].(string)
	if !nameOK || name == "" || !utf8.ValidString(name) || !kindOK ||
		!pathOK || !utf8.ValidString(filePath) || strings.ContainsRune(filePath, '\x00') ||
		!webControlOriginalFileAbsolutePath(filePath) || !mimeOK ||
		(kind != "html" && kind != "markdown") ||
		(kind == "html" && mimeType != "text/html") || (kind == "markdown" && mimeType != "text/markdown") {
		return nil
	}
	// JSON numbers arrive as float64. Do not coerce strings or truncate a
	// fractional value; Desktop file sizes must be nonnegative safe integers.
	size, ok := file["sizeBytes"].(float64)
	if !ok || math.IsNaN(size) || math.IsInf(size, 0) || size < 0 || size > (1<<53)-1 || math.Trunc(size) != size {
		return nil
	}
	return map[string]any{
		"fileName": name, "kind": kind, "path": filePath,
		"mimeType": mimeType, "sizeBytes": size,
	}
}

// The path is Desktop host data, not a Platform-local file to canonicalize.
// Preserve native macOS/POSIX paths and Windows drive/UNC paths byte-for-byte.
func webControlOriginalFileAbsolutePath(value string) bool {
	if strings.HasPrefix(value, "/") {
		return true
	}
	if len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	if strings.HasPrefix(value, `\\`) {
		parts := strings.Split(value[2:], `\`)
		return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
	}
	return false
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
		if root, relative := webControlPanelFile(descriptor); root != "" && relative != "" {
			out["url"] = webControlTarget{root: root, relativePath: relative}.fileURL()
		}
		if name, _ := descriptor["fileName"].(string); name != "" {
			out["fileName"] = name
		}
	default:
		out["kind"] = kind
		if root, relative := webControlPanelFile(descriptor); root != "" && relative != "" {
			out["kind"] = "file"
			out["url"] = webControlTarget{root: root, relativePath: relative}.fileURL()
			out["fileName"] = path.Base(relative)
		}
	}
	// surfaceId is null while a page is still registering; read the state again.
	for _, key := range []string{"title", "closable", "pinned", "surfaceId", "capabilities"} {
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

// Keep this actionable browser state distinct from a transport failure. The
// Desktop host owns connection and Chat binding; Platform must not reconnect or
// choose a different browser when a connection is missing.
func webControlBrowserConnectionResult(result ToolExecutionResult) ToolExecutionResult {
	if result.ExitCode == 0 || result.Structured == nil {
		return result
	}
	details, _ := result.Structured["details"].(map[string]any)
	response, _ := result.Structured["response"].(map[string]any)
	hostError, _ := response["error"].(map[string]any)
	code, _ := hostError["code"].(string)
	if code == "" {
		code, _ = details["clientErrorType"].(string)
	}
	switch code {
	case "browser_connection_required", "browser_selection_required", "browser_target_locked", "browser_scope_locked":
	default:
		return result
	}
	structured := CloneMap(result.Structured)
	errorNode := cloneDesktopMapValue(structured["error"])
	if len(errorNode) == 0 {
		errorNode = CloneMap(hostError)
	}
	if errorNode == nil {
		errorNode = map[string]any{}
	}
	errorNode["code"] = code
	structured["error"] = errorNode
	result = webControlRestructured(result, structured)
	result.Error = code
	return result
}

func (t *RuntimeToolExecutor) webControlSurfaceBrowser(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "browser"); failed {
		return failure, nil
	}
	browser, failure, failed := webControlString(args, "browser", true)
	if failed {
		return failure, nil
	}
	if browser != "chrome" && browser != "workpanel" {
		return webControlInvalidArgs("browser must be chrome or workpanel; choose from the user's task, or ask the user when unclear", "browser"), nil
	}
	// Desktop's canonical Run and application grant decide whether this selection
	// is allowed. Model input never contains a scope, connection or source override.
	return t.webControlSurfaceCall(ctx, "Browser.select", "", map[string]any{"browser": browser}, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceOpen(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "url"); failed {
		return failure, nil
	}
	rawURL, failure, failed := webControlString(args, "url", true)
	if failed {
		return failure, nil
	}
	target := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return webControlInvalidArgs("url must be an http:// or https:// address; use workpanel_open for file previews", "url"), nil
	}
	if parsed.User != nil {
		return webControlInvalidArgs("url must not contain credentials", "url"), nil
	}
	result, err := t.webControlSurfaceCall(ctx, "Surface.openChrome", "", map[string]any{"url": target}, execCtx)
	if err != nil || webControlHostErrorCode(result) != "browser_backend_mismatch" {
		return result, err
	}
	// The Run is bound to the WorkPanel: open the same address there instead of asking for Chrome.
	return t.webControlPanelOpen(ctx, map[string]any{"url": target}, execCtx)
}

// webControlHostErrorCode reads the error code Desktop reported for a page request.
func webControlHostErrorCode(result ToolExecutionResult) string {
	if result.ExitCode == 0 || result.Structured == nil {
		return ""
	}
	response, _ := result.Structured["response"].(map[string]any)
	hostError, _ := response["error"].(map[string]any)
	if code, _ := hostError["code"].(string); code != "" {
		return code
	}
	details, _ := result.Structured["details"].(map[string]any)
	code, _ := details["clientErrorType"].(string)
	return code
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
	if itemID, ok := strings.CutPrefix(surfaceID, webControlItemSurfacePrefix); ok {
		// A tab without a live page is activated or closed as a WorkPanel item;
		// Desktop checks that it belongs to this Chat and may be closed.
		if itemID == "" {
			return webControlInvalidArgs("surfaceId is not a WorkPanel tab identity; call workpanel_state", "surfaceId"), nil
		}
		action := "desktop.workpanel.activateTab"
		if method == "Surface.close" {
			action = "desktop.workpanel.closeTab"
		}
		result, err := t.webControlAction(ctx, action, map[string]any{"tabId": itemID}, execCtx)
		return projectWebControlPanelResult(result), err
	}
	return t.webControlSurfaceCall(ctx, method, surfaceID, nil, execCtx)
}

// webControlSurfaceViewport emulates a device viewport on one live page, or
// restores the panel size with reset. The page keeps its surfaceId.
func (t *RuntimeToolExecutor) webControlSurfaceViewport(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "width", "height", "deviceScaleFactor", "mobile", "reset"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	reset, hasReset, failure, failed := webControlBool(args, "reset")
	if failed {
		return failure, nil
	}
	if hasReset {
		if !reset || len(args) != 2 {
			return webControlInvalidArgs("reset must be true and cannot be combined with width, height, deviceScaleFactor or mobile", "reset"), nil
		}
		return t.webControlSurfaceCall(ctx, "Surface.setViewport", surfaceID, map[string]any{"reset": true}, execCtx)
	}
	params := map[string]any{}
	for _, field := range []string{"width", "height"} {
		value, ok := args[field].(float64)
		if !ok || value != math.Trunc(value) {
			return webControlInvalidArgs(field+" is required and must be an integer number of CSS pixels", field), nil
		}
		params[field] = value
	}
	if value, present := args["deviceScaleFactor"]; present {
		if _, ok := value.(float64); !ok {
			return webControlInvalidArgs("deviceScaleFactor must be a number", "deviceScaleFactor"), nil
		}
		params["deviceScaleFactor"] = value
	}
	mobile, hasMobile, failure, failed := webControlBool(args, "mobile")
	if failed {
		return failure, nil
	}
	if hasMobile {
		params["mobile"] = mobile
	}
	return t.webControlSurfaceCall(ctx, "Surface.setViewport", surfaceID, params, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceObserve(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "maxTextLength", "maxElements", "viewportOnly"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	var params map[string]any
	for _, limit := range []struct {
		field string
		max   int
	}{{"maxTextLength", 30000}, {"maxElements", 500}} {
		if raw, present := args[limit.field]; present {
			number, valid := webControlNumber(raw)
			if !valid || number < 1 || number > float64(limit.max) || math.Trunc(number) != number {
				return webControlInvalidArgs(fmt.Sprintf("%s must be an integer in range 1–%d", limit.field, limit.max), limit.field), nil
			}
			if params == nil {
				params = map[string]any{}
			}
			params[limit.field] = number
		}
	}
	if viewportOnly, present, failure, failed := webControlBool(args, "viewportOnly"); failed {
		return failure, nil
	} else if present {
		if params == nil {
			params = map[string]any{}
		}
		params["viewportOnly"] = viewportOnly
	}
	return t.webControlSurfaceCall(ctx, "Surface.observe", surfaceID, params, execCtx)
}

func webControlNumber(raw any) (float64, bool) {
	var number float64
	switch value := raw.(type) {
	case float64:
		number = value
	case int:
		number = float64(value)
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func (t *RuntimeToolExecutor) webControlSurfaceScroll(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "deltaY", "deltaX"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	params := map[string]any{}
	for _, field := range []string{"deltaY", "deltaX"} {
		raw, present := args[field]
		if !present && field == "deltaX" {
			continue
		}
		number, valid := webControlNumber(raw)
		if !valid || number < -2000 || number > 2000 {
			return webControlInvalidArgs(field+" must be a finite number in range -2000–2000", field), nil
		}
		params[field] = number
	}
	return t.webControlSurfaceCall(ctx, "Surface.scroll", surfaceID, params, execCtx)
}

func (t *RuntimeToolExecutor) webControlSurfaceDialog(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "observationId", "dialogId", "action", "promptText"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	params := map[string]any{}
	for _, field := range []string{"observationId", "dialogId", "action"} {
		value, failure, failed := webControlString(args, field, true)
		if failed {
			return failure, nil
		}
		params[field] = value
	}
	if params["action"] != "accept" && params["action"] != "dismiss" {
		return webControlInvalidArgs("action must be accept or dismiss", "action"), nil
	}
	if raw, present := args["promptText"]; present {
		value, valid := raw.(string)
		if !valid || params["action"] != "accept" {
			return webControlInvalidArgs("promptText must be a string and is only valid with action accept for a prompt dialog", "promptText"), nil
		}
		params["promptText"] = value
	}
	return t.webControlSurfaceCall(ctx, "Surface.handleDialog", surfaceID, params, execCtx)
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

func (t *RuntimeToolExecutor) webControlSurfaceClick(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "selector", "x", "y", "ref", "observationId", "waitFor", "timeoutMs", "captureDownload"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	_, hasSelector := args["selector"]
	_, hasX := args["x"]
	_, hasY := args["y"]
	ref, observationID, hasRef, failure, failed := webControlObservationReference(args)
	if failed {
		return failure, nil
	}
	if hasRef && (hasSelector || hasX || hasY) {
		return webControlInvalidArgs("ref and observationId cannot be combined with selector or coordinates", "ref"), nil
	}
	if !hasRef && (hasSelector == (hasX || hasY) || hasX != hasY) {
		return webControlInvalidArgs("provide either selector or both x and y", ""), nil
	}
	params := map[string]any{}
	method := "Input.click"
	if hasRef {
		method = "Surface.click"
		params["ref"], params["observationId"] = ref, observationID
	} else if hasSelector {
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
	if timeout, present := args["timeoutMs"]; present {
		if !hasRef {
			return webControlInvalidArgs("timeoutMs is only valid with ref and observationId", "timeoutMs"), nil
		}
		number, valid := webControlNumber(timeout)
		if !valid || number < 100 || number > 10000 || math.Trunc(number) != number {
			return webControlInputError(toolinput.New("timeoutMs", "JSON integer in range 100–10000", timeout, true, "Provide an integer in the allowed range.")), nil
		}
		params["timeoutMs"] = number
	}
	if capture, present, failure, failed := webControlBool(args, "captureDownload"); failed {
		return failure, nil
	} else if present {
		params["captureDownload"] = capture
	}
	return t.webControlSurfaceCall(ctx, method, surfaceID, params, execCtx)
}

// Element references are valid only in the observation that issued them. Both
// fields are opaque host identifiers; neither is a selector or a browser tab ID.
func webControlObservationReference(args map[string]any) (string, string, bool, ToolExecutionResult, bool) {
	_, hasRef := args["ref"]
	_, hasObservation := args["observationId"]
	if hasRef != hasObservation {
		missing := "observationId"
		if !hasRef {
			missing = "ref"
		}
		return "", "", false, webControlInvalidArgs("ref and observationId must be provided together", missing), true
	}
	if !hasRef {
		return "", "", false, ToolExecutionResult{}, false
	}
	ref, failure, failed := webControlString(args, "ref", true)
	if failed {
		return "", "", false, failure, true
	}
	observationID, failure, failed := webControlString(args, "observationId", true)
	if failed {
		return "", "", false, failure, true
	}
	return ref, observationID, true, ToolExecutionResult{}, false
}

func (t *RuntimeToolExecutor) webControlSurfaceElement(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "selector", "ref", "observationId", "action", "value", "values", "key"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	ref, observationID, hasRef, failure, failed := webControlObservationReference(args)
	if failed {
		return failure, nil
	}
	if _, hasSelector := args["selector"]; hasRef && hasSelector {
		return webControlInvalidArgs("ref and observationId cannot be combined with selector", "ref"), nil
	}
	selector, failure, failed := webControlString(args, "selector", !hasRef)
	if failed {
		return failure, nil
	}
	action, failure, failed := webControlString(args, "action", true)
	if failed {
		return failure, nil
	}
	if !webControlElementActions[action] {
		return webControlInvalidArgs("action must be fill, select, focus, scroll, hover, type, press, check or uncheck; use surface_click to click", "action"), nil
	}
	actionArgs := map[string]any{"action": action}
	if hasRef {
		actionArgs["ref"], actionArgs["observationId"] = ref, observationID
	} else {
		actionArgs["selector"] = selector
	}
	if raw, present := args["value"]; present {
		value, ok := raw.(string)
		if !ok {
			return webControlInvalidArgs("value must be a string", "value"), nil
		}
		actionArgs["value"] = value
	}
	_, hasValues := args["values"]
	if hasValues {
		if _, hasValue := args["value"]; hasValue || action != "select" {
			return webControlInvalidArgs("values is only valid for select and is mutually exclusive with value", "values"), nil
		}
		values, valid := webControlStringArray(args["values"], 100, 4096)
		if !valid {
			return webControlInvalidArgs("values must be an array of at most 100 strings, each at most 4096 characters", "values"), nil
		}
		actionArgs["values"] = values
	}
	if raw, present := args["key"]; present {
		key, valid := raw.(string)
		if !valid || action != "press" || !webControlPressKeys[key] {
			return webControlInvalidArgs("key must be an allowed press key and is only valid with action press", "key"), nil
		}
		actionArgs["key"] = key
	}
	if action == "press" {
		if _, present := actionArgs["key"]; !present {
			return webControlInvalidArgs("key is required with action press", "key"), nil
		}
	}
	if action == "type" {
		if _, present := actionArgs["value"]; !present {
			return webControlInvalidArgs("value is required with action type", "value"), nil
		}
	}
	if _, present := args["value"]; present && action != "fill" && action != "select" && action != "type" {
		return webControlInvalidArgs("value is only valid with fill, select or type", "value"), nil
	}
	// WorkPanel and Chrome share one typed interaction contract in Desktop.
	return t.webControlSurfaceCall(ctx, "Surface.interact", surfaceID, actionArgs, execCtx)
}

func webControlStringArray(raw any, maxItems, maxChars int) ([]string, bool) {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, len(typed))
		for index, item := range typed {
			value, valid := item.(string)
			if !valid {
				return nil, false
			}
			values[index] = value
		}
	default:
		return nil, false
	}
	if len(values) > maxItems {
		return nil, false
	}
	for _, value := range values {
		if utf8.RuneCountInString(value) > maxChars {
			return nil, false
		}
	}
	return values, true
}

// webControlSurfaceLogin asks Desktop to use a password the user saved for the
// page's own site. The site is never an argument: Desktop reads it from the
// page. The result carries masked account names and a status, never a password.
func (t *RuntimeToolExecutor) webControlSurfaceLogin(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "action", "accountId", "submit"); failed {
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
	accountID, failure, failed := webControlString(args, "accountId", false)
	if failed {
		return failure, nil
	}
	submit, submitSet, failure, failed := webControlBool(args, "submit")
	if failed {
		return failure, nil
	}
	actionArgs := map[string]any{"surfaceId": surfaceID}
	switch action {
	case "list":
		if accountID != "" || submitSet {
			return webControlInvalidArgs("accountId and submit are valid only with action fill", "action"), nil
		}
		return t.webControlAction(ctx, "desktop.credentials.list", actionArgs, execCtx)
	case "fill":
		if accountID != "" {
			actionArgs["accountId"] = strings.TrimSpace(accountID)
		}
		if submitSet {
			actionArgs["submit"] = submit
		}
		return t.webControlAction(ctx, "desktop.credentials.fill", actionArgs, execCtx)
	default:
		return webControlInvalidArgs("action must be list or fill", "action"), nil
	}
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
