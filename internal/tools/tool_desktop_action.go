package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	. "agent-platform-runner-go/internal/contracts"
)

const defaultDesktopActionBridgeURL = "http://127.0.0.1:11788/actions/call"

var desktopActionAllowlist = map[string]bool{
	"desktop.page.getContext":                  true,
	"desktop.page.getFormState":                true,
	"desktop.page.validateForm":                true,
	"desktop.page.previewPatch":                true,
	"desktop.page.applyPatch":                  true,
	"desktop.navigate.toRoute":                 true,
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
	"desktop.automations.listSchedules":        true,
	"desktop.automations.getScheduleDetail":    true,
	"desktop.automations.validateSchedule":     true,
	"desktop.automations.previewSchedule":      true,
	"desktop.automations.createSchedule":       true,
	"desktop.automations.updateSchedule":       true,
	"desktop.automations.pauseSchedule":        true,
	"desktop.automations.resumeSchedule":       true,
	"desktop.automations.deleteSchedule":       true,
	"desktop.automations.explainNextRun":       true,
	"desktop.memory.getSettings":               true,
	"desktop.memory.getSummary":                true,
	"desktop.memory.listRecentItems":           true,
	"desktop.memory.searchItems":               true,
	"desktop.memory.previewItem":               true,
	"desktop.memory.enableAutoLearn":           true,
	"desktop.memory.disableAutoLearn":          true,
	"desktop.embeddedWeb.listSurfaces":         true,
	"desktop.embeddedWeb.getActiveSurface":     true,
	"desktop.embeddedWeb.activateSurface":      true,
	"desktop.embeddedWeb.getPageContext":       true,
	"desktop.embeddedWeb.navigate":             true,
	"desktop.embeddedWeb.reload":               true,
	"desktop.embeddedWeb.goBack":               true,
	"desktop.embeddedWeb.openTab":              true,
	"desktop.embeddedWeb.closeTab":             true,
	"desktop.embeddedWeb.switchTab":            true,
	"desktop.embeddedWeb.readPageData":         true,
	"desktop.embeddedWeb.extractStructured":    true,
	"desktop.embeddedWeb.interactElement":      true,
	"desktop.embeddedWeb.executeScript":        true,
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
	body, err := json.Marshal(payload)
	if err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), nil), nil
	}

	bridgeURL := desktopActionBridgeURL()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeURL, bytes.NewReader(body))
	if err != nil {
		return desktopActionErrorResult("invalid_bridge_url", err.Error(), map[string]any{"bridgeUrl": bridgeURL}), nil
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return desktopActionErrorResult("desktop_action_bridge_unavailable", err.Error(), map[string]any{"bridgeUrl": bridgeURL}), nil
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

func desktopActionBridgeURL() string {
	if value := strings.TrimSpace(os.Getenv("DESKTOP_ACTION_BRIDGE_URL")); value != "" {
		return value
	}
	return defaultDesktopActionBridgeURL
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
