package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopCdpCaptureScreenshotMethod = "Page.captureScreenshot"
const desktopCdpScreenshotMimeType = "image/png"

const (
	webClientSidebarGetState   = "webclient.sidebar.getState"
	webClientSidebarOpenURL    = "webclient.sidebar.openUrl"
	webClientSidebarRefreshURL = "webclient.sidebar.refreshUrl"
	webClientSidebarSetState   = "webclient.sidebar.setState"
)

const (
	webClientSidebarURLMaxLength   = 2048
	webClientSidebarTitleMaxLength = 200
)

var desktopCdpBooleanParamKeys = map[string]bool{
	"allowUnsafeEvalBlockedByCSP": true,
	"autoAttach":                  true,
	"autoRepeat":                  true,
	"awaitPromise":                true,
	"background":                  true,
	"captureBeyondViewport":       true,
	"disableBreaks":               true,
	"discover":                    true,
	"dontSetVisibleSize":          true,
	"exclude":                     true,
	"flatten":                     true,
	"fromSurface":                 true,
	"generatePreview":             true,
	"hasTouch":                    true,
	"hidden":                      true,
	"ignoreCache":                 true,
	"includeCommandLineAPI":       true,
	"isKeypad":                    true,
	"isMobile":                    true,
	"isSystemKey":                 true,
	"landscape":                   true,
	"newWindow":                   true,
	"optimizeForSpeed":            true,
	"pierce":                      true,
	"replMode":                    true,
	"reportDirectSocketTraffic":   true,
	"returnByValue":               true,
	"silent":                      true,
	"throwOnSideEffect":           true,
	"userGesture":                 true,
	"waitForDebuggerOnStart":      true,
}

var (
	desktopActionAllowlistOnce sync.Once
	desktopActionAllowlist     map[string]bool
	desktopActionAllowlistErr  error
	webClientActionRequestSeq  atomic.Uint64
)

func getDesktopActionAllowlist() (map[string]bool, error) {
	desktopActionAllowlistOnce.Do(func() {
		desktopActionAllowlist, desktopActionAllowlistErr = loadDesktopActionAllowlist()
	})
	return desktopActionAllowlist, desktopActionAllowlistErr
}

func loadDesktopActionAllowlist() (map[string]bool, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		if def.Name != "desktop_action" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("desktop_action schema missing properties")
		}
		actionProperty, ok := properties["action"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("desktop_action schema missing action property")
		}
		enum, ok := actionProperty["enum"].([]any)
		if !ok || len(enum) == 0 {
			return nil, fmt.Errorf("desktop_action action enum is required")
		}
		allowlist := make(map[string]bool, len(enum))
		for _, item := range enum {
			action, ok := item.(string)
			if !ok || strings.TrimSpace(action) == "" {
				return nil, fmt.Errorf("desktop_action action enum contains an invalid value")
			}
			action = strings.TrimSpace(action)
			if allowlist[action] {
				return nil, fmt.Errorf("desktop_action action enum contains duplicate value %q", action)
			}
			allowlist[action] = true
		}
		return allowlist, nil
	}
	return nil, fmt.Errorf("desktop_action tool definition not found")
}

type desktopActionRequest struct {
	RequestID string              `json:"requestId,omitempty"`
	Action    string              `json:"action"`
	Args      map[string]any      `json:"args"`
	Source    desktopActionSource `json:"source,omitempty"`
}

type desktopActionSource struct {
	RunID    string `json:"runId,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	AgentKey string `json:"agentKey,omitempty"`
}

type desktopCDPRequest struct {
	RequestID string              `json:"requestId,omitempty"`
	Method    string              `json:"method"`
	Params    map[string]any      `json:"params,omitempty"`
	TargetID  string              `json:"targetId,omitempty"`
	SessionID string              `json:"sessionId,omitempty"`
	SurfaceID string              `json:"surfaceId,omitempty"`
	Source    desktopActionSource `json:"source,omitempty"`
}

func (t *RuntimeToolExecutor) invokeDesktopAction(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	action := strings.TrimSpace(stringArg(args, "action"))
	if action == "" {
		return desktopActionErrorResult("invalid_args", "action is required", nil), nil
	}
	allowlist, err := getDesktopActionAllowlist()
	if err != nil {
		return desktopActionErrorResult("desktop_action_allowlist_unavailable", "desktop action allowlist is unavailable", map[string]any{"error": err.Error()}), nil
	}
	if !allowlist[action] {
		return desktopActionErrorResult("unknown_action", "desktop action is not allowlisted", map[string]any{"action": action}), nil
	}

	rawActionArgs, hasActionArgs := args["args"]
	actionArgs, ok := rawActionArgs.(map[string]any)
	if strings.HasPrefix(action, "webclient.") && hasActionArgs && !ok {
		return desktopActionErrorResult("invalid_args", "webclient action args must be an object", map[string]any{"action": action}), nil
	}
	if !ok || actionArgs == nil {
		actionArgs = map[string]any{}
	}
	if strings.HasPrefix(action, "webclient.") {
		return t.invokeWebClientAction(ctx, action, actionArgs, strings.TrimSpace(stringArg(args, "requestId")), execCtx)
	}
	if summary := strings.TrimSpace(stringArg(args, "confirmationSummary")); summary != "" {
		actionArgs["confirmationSummary"] = summary
	}

	payload := desktopActionRequest{
		RequestID: strings.TrimSpace(stringArg(args, "requestId")),
		Action:    action,
		Args:      actionArgs,
		Source:    buildDesktopActionSource(execCtx),
	}
	return t.invokeDesktopBridge(ctx, t.cfg.Desktop.Action, payload, "desktop_action")
}

func (t *RuntimeToolExecutor) invokeWebClientAction(
	ctx context.Context,
	action string,
	actionArgs map[string]any,
	requestID string,
	execCtx *ExecutionContext,
) (ToolExecutionResult, error) {
	if validationErr := validateWebClientActionArgs(action, actionArgs); validationErr != "" {
		return desktopActionErrorResult("invalid_args", validationErr, map[string]any{"action": action}), nil
	}
	if t == nil || t.webClientAction == nil {
		return desktopActionErrorResult("desktop_action_provider_unavailable", "webclient action provider is unavailable", map[string]any{"action": action}), nil
	}
	target := WebClientTarget{}
	if execCtx != nil {
		if t.webClientTargets != nil {
			if current, ok := t.webClientTargets.ResolveWebClientTarget(execCtx.Session.RunID); ok {
				target = current
			}
		} else {
			target = execCtx.Session.WebClientTarget
		}
	}
	if target.IsZero() {
		return desktopActionErrorResult("desktop_action_target_unavailable", "webclient target is unavailable for this run", map[string]any{
			"action": action,
			"reason": "run_target_missing",
		}), nil
	}
	if requestID == "" {
		requestID = fmt.Sprintf("wsa-%d", webClientActionRequestSeq.Add(1))
	}
	timeout := time.Duration(t.cfg.Desktop.Action.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := t.webClientAction.InvokeWebClientAction(requestCtx, target, WebClientActionRequest{
		ID:      requestID,
		Type:    action,
		Payload: cloneDesktopMap(actionArgs),
	})
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return desktopActionErrorResult("desktop_action_client_timeout", "webclient action request timed out", map[string]any{"action": action}), nil
		case errors.Is(err, ErrWebClientTargetUnavailable):
			return desktopActionErrorResult("desktop_action_target_unavailable", "webclient target is unavailable for this run", map[string]any{
				"action": action,
				"reason": "target_connection_unavailable",
			}), nil
		case errors.Is(err, ErrWebClientDisconnected):
			return desktopActionErrorResult("desktop_action_client_disconnected", "webclient disconnected before completing the action", map[string]any{"action": action}), nil
		default:
			return desktopActionErrorResult("desktop_action_invalid_client_response", err.Error(), map[string]any{"action": action}), nil
		}
	}
	if response.ID != requestID {
		return desktopActionErrorResult("desktop_action_invalid_client_response", "webclient response id does not match request", map[string]any{"action": action}), nil
	}
	switch strings.ToLower(strings.TrimSpace(response.Frame)) {
	case "error":
		if strings.TrimSpace(response.Type) == "" || response.Code == nil || *response.Code <= 0 {
			return desktopActionErrorResult("desktop_action_invalid_client_response", "webclient error frame must include a type and positive code", map[string]any{"action": action}), nil
		}
		clientData, dataErr := decodeWebClientActionData(response.Data)
		if dataErr != nil {
			return desktopActionErrorResult("desktop_action_invalid_client_response", dataErr.Error(), map[string]any{"action": action}), nil
		}
		details := map[string]any{
			"action":          action,
			"clientErrorType": response.Type,
			"clientCode":      *response.Code,
		}
		if clientData != nil {
			details["clientData"] = clientData
		}
		return desktopActionErrorResult("desktop_action_client_rejected", firstDesktopActionMessage(response.Msg, "webclient rejected the action"), details), nil
	case "response":
		if response.Type != action {
			return desktopActionErrorResult("desktop_action_invalid_client_response", "webclient response type does not match action", map[string]any{"action": action}), nil
		}
		if response.Code == nil {
			return desktopActionErrorResult("desktop_action_invalid_client_response", "webclient response frame must include code", map[string]any{"action": action}), nil
		}
		if *response.Code != 0 {
			return desktopActionErrorResult("desktop_action_client_rejected", firstDesktopActionMessage(response.Msg, "webclient rejected the action"), map[string]any{
				"action":     action,
				"clientCode": *response.Code,
			}), nil
		}
	default:
		return desktopActionErrorResult("desktop_action_invalid_client_response", "webclient returned an unsupported frame", map[string]any{"action": action}), nil
	}
	resultData, dataErr := decodeWebClientActionData(response.Data)
	if dataErr != nil {
		return desktopActionErrorResult("desktop_action_invalid_client_response", dataErr.Error(), map[string]any{"action": action}), nil
	}
	if resultData == nil {
		resultData = map[string]any{}
	}
	return structuredResultWithExit(resultData, 0), nil
}

func decodeWebClientActionData(data json.RawMessage) (map[string]any, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, errors.New("webclient response data must be an object")
	}
	return decoded, nil
}

func validateWebClientActionArgs(action string, args map[string]any) string {
	switch action {
	case webClientSidebarGetState:
		if len(args) != 0 {
			return "webclient.sidebar.getState args must be empty"
		}
		return ""
	case webClientSidebarSetState:
		for key := range args {
			switch key {
			case "sidebar", "open", "tab":
			default:
				return "webclient.sidebar.setState contains an unsupported argument: " + key
			}
		}
		sidebar, ok := args["sidebar"].(string)
		if !ok || (sidebar != "left" && sidebar != "right") {
			return "sidebar must be left or right"
		}
		open, ok := args["open"].(bool)
		if !ok {
			return "open must be a boolean"
		}
		tab, hasTab := args["tab"]
		if sidebar == "left" && hasTab {
			return "tab is not supported for the left sidebar"
		}
		if !open && hasTab {
			return "tab is not supported when closing a sidebar"
		}
		if hasTab {
			tabName, ok := tab.(string)
			if !ok || (tabName != "overview" && tabName != "btw" && tabName != "debug") {
				return "tab must be overview, btw, or debug"
			}
		}
		return ""
	case webClientSidebarOpenURL:
		for key := range args {
			switch key {
			case "url", "title":
			default:
				return "webclient.sidebar.openUrl contains an unsupported argument: " + key
			}
		}
		if validationErr := validateWebClientSidebarURLArg(args); validationErr != "" {
			return validationErr
		}
		if title, hasTitle := args["title"]; hasTitle {
			titleText, ok := title.(string)
			if !ok {
				return "title must be a string"
			}
			if utf8.RuneCountInString(strings.TrimSpace(titleText)) > webClientSidebarTitleMaxLength {
				return fmt.Sprintf("title must be at most %d characters", webClientSidebarTitleMaxLength)
			}
		}
		return ""
	case webClientSidebarRefreshURL:
		for key := range args {
			if key != "url" {
				return "webclient.sidebar.refreshUrl contains an unsupported argument: " + key
			}
		}
		return validateWebClientSidebarURLArg(args)
	default:
		return "unsupported webclient action"
	}
}

func validateWebClientSidebarURLArg(args map[string]any) string {
	rawURL, ok := args["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return "url must be a non-empty string"
	}
	if utf8.RuneCountInString(strings.TrimSpace(rawURL)) > webClientSidebarURLMaxLength {
		return fmt.Sprintf("url must be at most %d characters", webClientSidebarURLMaxLength)
	}
	return validateWebClientSidebarURL(rawURL)
}

func validateWebClientSidebarURL(rawURL string) string {
	candidate := strings.TrimSpace(rawURL)
	if strings.HasPrefix(candidate, "//") {
		return "url must be an absolute http or https URL"
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" {
		parsed, err = url.Parse("https://" + candidate)
	}
	if err != nil ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		parsed.Hostname() == "" {
		return "url must be a valid http or https URL"
	}
	if parsed.User != nil {
		return "url must not contain credentials"
	}
	return ""
}

func firstDesktopActionMessage(message string, fallback string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	return fallback
}

func (t *RuntimeToolExecutor) invokeDesktopCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	method := strings.TrimSpace(stringArg(args, "method"))
	if method == "" {
		return desktopActionErrorResult("invalid_args", "method is required", nil), nil
	}
	params, ok := args["params"].(map[string]any)
	if !ok || params == nil {
		params = map[string]any{}
	}
	payload := desktopCDPRequest{
		RequestID: strings.TrimSpace(stringArg(args, "requestId")),
		Method:    method,
		Params:    normalizeDesktopCDPParams(params),
		TargetID:  strings.TrimSpace(stringArg(args, "targetId")),
		SessionID: strings.TrimSpace(stringArg(args, "sessionId")),
		SurfaceID: strings.TrimSpace(stringArg(args, "surfaceId")),
		Source:    buildDesktopActionSource(execCtx),
	}
	result, err := t.invokeDesktopBridge(ctx, t.cfg.Desktop.CDP, payload, "desktop_cdp")
	if err != nil || method != desktopCdpCaptureScreenshotMethod || result.ExitCode != 0 {
		return result, err
	}
	return t.storeDesktopCdpScreenshot(result, execCtx), nil
}

func normalizeDesktopCDPParams(params map[string]any) map[string]any {
	normalized := make(map[string]any, len(params))
	for key, value := range params {
		normalized[key] = normalizeDesktopCDPParamValue(key, value)
	}
	return normalized
}

func normalizeDesktopCDPParamValue(key string, value any) any {
	if desktopCdpBooleanParamKeys[key] {
		if boolValue, ok := parseDesktopCDPStringBool(value); ok {
			return boolValue
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		return normalizeDesktopCDPParams(typed)
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeDesktopCDPParamValue("", item)
		}
		return normalized
	default:
		return value
	}
}

func parseDesktopCDPStringBool(value any) (bool, bool) {
	raw, ok := value.(string)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (t *RuntimeToolExecutor) invokeDesktopBridge(ctx context.Context, bridge config.DesktopBridgeConfig, payload any, toolName string) (ToolExecutionResult, error) {
	bridgeURL := strings.TrimSpace(bridge.BridgeURL)
	if bridgeURL == "" {
		return desktopActionErrorResult(toolName+"_bridge_not_configured", "desktop bridge is not configured", nil), nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), nil), nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeURL, bytes.NewReader(body))
	if err != nil {
		return desktopActionErrorResult("invalid_bridge_url", err.Error(), map[string]any{"bridgeUrl": bridgeURL}), nil
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	timeout := time.Duration(bridge.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return desktopActionErrorResult(toolName+"_bridge_unavailable", err.Error(), map[string]any{"bridgeUrl": bridgeURL}), nil
	}
	defer response.Body.Close()

	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return desktopActionErrorResult("invalid_bridge_response", err.Error(), map[string]any{
			"bridgeUrl":  bridgeURL,
			"statusCode": response.StatusCode,
		}), nil
	}

	exitCode := 0
	if response.StatusCode < 200 || response.StatusCode >= 300 || decoded["ok"] == false {
		exitCode = -1
	}
	return structuredResultWithExit(map[string]any{
		"bridgeUrl":  bridgeURL,
		"statusCode": response.StatusCode,
		"response":   decoded,
	}, exitCode), nil
}

func (t *RuntimeToolExecutor) storeDesktopCdpScreenshot(result ToolExecutionResult, execCtx *ExecutionContext) ToolExecutionResult {
	payload := cloneDesktopMap(result.Structured)
	response := cloneDesktopMapValue(payload["response"])
	resultNode := cloneDesktopMapValue(response["result"])
	data, _ := resultNode["data"].(string)
	if strings.TrimSpace(data) == "" {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_data_missing", "Page.captureScreenshot response.result.data is missing", nil)
	}

	chatID := desktopCdpScreenshotChatID(execCtx)
	if strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" || !chat.ValidChatID(chatID) {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_context_unavailable", "chat context and cfg.Paths.ChatsDir are required to save desktop_cdp screenshots", map[string]any{
			"chatId":      chatID,
			"hasChatsDir": strings.TrimSpace(t.cfg.Paths.ChatsDir) != "",
		})
	}

	imageBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_decode_failed", err.Error(), nil)
	}

	referenceName := desktopCdpScreenshotReferenceName(time.Now().UTC())
	chatDir := filepath.Join(t.cfg.Paths.ChatsDir, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_write_failed", err.Error(), nil)
	}
	filePath := filepath.Join(chatDir, referenceName)
	if err := os.WriteFile(filePath, imageBytes, 0o600); err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_write_failed", err.Error(), nil)
	}

	sha := sha256.Sum256(imageBytes)
	resultNode["data"] = map[string]any{
		"saved":                true,
		"dataOmitted":          true,
		"referenceName":        referenceName,
		"filePath":             filePath,
		"mimeType":             desktopCdpScreenshotMimeType,
		"sizeBytes":            len(imageBytes),
		"sha256":               hex.EncodeToString(sha[:]),
		"visionRecognizeImage": map[string]any{"reference_name": referenceName},
	}
	response["result"] = resultNode
	payload["response"] = response
	return structuredResult(payload)
}

func desktopCdpScreenshotChatID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	if chatID := strings.TrimSpace(execCtx.Request.ChatID); chatID != "" {
		return chatID
	}
	return strings.TrimSpace(execCtx.Session.ChatID)
}

func desktopCdpScreenshotReferenceName(now time.Time) string {
	return fmt.Sprintf("desktop-cdp-screenshot-%s%09dZ.png", now.Format("20060102T150405"), now.Nanosecond())
}

func desktopCdpScreenshotErrorResult(payload map[string]any, code string, message string, details map[string]any) ToolExecutionResult {
	sanitizeDesktopCdpScreenshotData(payload)
	payload["error"] = map[string]any{
		"code":    code,
		"message": message,
	}
	if details != nil {
		payload["details"] = details
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}

func sanitizeDesktopCdpScreenshotData(payload map[string]any) {
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return
	}
	resultNode, ok := response["result"].(map[string]any)
	if !ok {
		return
	}
	if _, ok := resultNode["data"].(string); ok {
		resultNode["data"] = map[string]any{
			"saved":       false,
			"dataOmitted": true,
		}
	}
}

func cloneDesktopMapValue(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return cloneDesktopMap(mapped)
	}
	return map[string]any{}
}

func cloneDesktopMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		if mapped, ok := value.(map[string]any); ok {
			out[key] = cloneDesktopMap(mapped)
			continue
		}
		out[key] = value
	}
	return out
}

func buildDesktopActionSource(execCtx *ExecutionContext) desktopActionSource {
	if execCtx == nil {
		return desktopActionSource{}
	}
	return desktopActionSource{
		RunID:    execCtx.Session.RunID,
		ChatID:   execCtx.Session.ChatID,
		AgentKey: execCtx.Session.AgentKey,
	}
}

func desktopActionErrorResult(code string, message string, details map[string]any) ToolExecutionResult {
	payload := map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if details != nil {
		payload["details"] = details
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}
