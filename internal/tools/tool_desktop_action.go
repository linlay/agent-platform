package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopCdpCaptureScreenshotMethod = "Page.captureScreenshot"
const desktopCdpScreenshotMimeType = "image/png"

var (
	desktopActionAllowlistOnce sync.Once
	desktopActionAllowlist     map[string]bool
	desktopActionAllowlistErr  error
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

	actionArgs, ok := args["args"].(map[string]any)
	if !ok || actionArgs == nil {
		actionArgs = map[string]any{}
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
		Params:    params,
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
