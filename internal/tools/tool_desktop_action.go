package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

var desktopActionAllowlist = map[string]bool{
	"desktop.navigate.toRoute":                 true,
	"desktop.settings.getState":                true,
	"desktop.settings.validatePatch":           true,
	"desktop.settings.previewPatch":            true,
	"desktop.settings.applyPatch":              true,
	"desktop.controlCenter.listServices":       true,
	"desktop.controlCenter.getServiceStatus":   true,
	"desktop.controlCenter.getServiceDetail":   true,
	"desktop.controlCenter.getServiceLogsMeta": true,
	"desktop.controlCenter.readServiceLog":     true,
	"desktop.controlCenter.openLogViewer":      true,
	"desktop.controlCenter.installService":     true,
	"desktop.controlCenter.initializeService":  true,
	"desktop.controlCenter.startService":       true,
	"desktop.controlCenter.stopService":        true,
	"desktop.controlCenter.restartService":     true,
	"desktop.market.getSettings":               true,
	"desktop.market.validateSettings":          true,
	"desktop.market.previewSettingsPatch":      true,
	"desktop.market.applySettingsPatch":        true,
	"desktop.market.listItems":                 true,
	"desktop.market.refresh":                   true,
	"desktop.market.getItemDetail":             true,
	"desktop.market.installItem":               true,
	"desktop.market.updateItem":                true,
	"desktop.market.uninstallItem":             true,
	"desktop.market.importSkill":               true,
	"desktop.market.buildSandboxImage":         true,
	"desktop.help.getCurrentTopic":             true,
	"desktop.help.searchTopics":                true,
	"desktop.help.openTopic":                   true,
	"desktop.help.explainCurrentPage":          true,
	"desktop.help.suggestNextAction":           true,
	"desktop.help.navigateToRelatedPage":       true,
	"desktop.agents.listAgents":                true,
	"desktop.agents.getAgentDetail":            true,
	"desktop.agents.validateAgentConfig":       true,
	"desktop.agents.previewAgentConfigPatch":   true,
	"desktop.agents.applyAgentConfigPatch":     true,
	"desktop.agents.createAgentDraft":          true,
	"desktop.agents.createAgent":               true,
	"desktop.agents.updateAgent":               true,
	"desktop.agents.cloneAgent":                true,
	"desktop.agents.disableAgent":              true,
	"desktop.agents.reloadAgents":              true,
	"desktop.automations.listAutomations":      true,
	"desktop.automations.getAutomationDetail":  true,
	"desktop.automations.validateAutomation":   true,
	"desktop.automations.previewAutomation":    true,
	"desktop.automations.createAutomation":     true,
	"desktop.automations.updateAutomation":     true,
	"desktop.automations.pauseAutomation":      true,
	"desktop.automations.resumeAutomation":     true,
	"desktop.automations.deleteAutomation":     true,
	"desktop.automations.explainNextRun":       true,
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
	if !desktopActionAllowlist[action] {
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
	return t.invokeDesktopBridge(ctx, t.cfg.Desktop.CDP, payload, "desktop_cdp")
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

	timeout := time.Duration(bridge.RequestTimeoutMs) * time.Millisecond
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
