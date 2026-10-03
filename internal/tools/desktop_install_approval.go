package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
)

func desktopInstallAction(tool, action string) bool {
	return tool == "desktop_market" && (action == "market.installItem" || action == "market.updateItem") || tool == "desktop_webapp" && action == "webapp.install"
}

func desktopInstallFingerprint(e *ExecutionContext, tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	digest := sha256.Sum256(raw)
	return ToolApprovalFingerprint(e, tool, AnyStringNode(args["action"]), hex.EncodeToString(digest[:]))
}

// PrepareToolApproval prepares operation intent; package metadata is validated by
// Desktop during installation, not inferred from model-provided names/versions.
func (t *RuntimeToolExecutor) PrepareToolApproval(_ context.Context, tool string, args map[string]any, e *ExecutionContext) (*ToolApproval, error) {
	action := AnyStringNode(args["action"])
	if !desktopInstallAction(tool, action) {
		return nil, nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return nil, fmt.Errorf("Desktop installation requires Desktop runtime")
	}
	if e == nil || e.CurrentToolID == "" {
		return nil, fmt.Errorf("installation requires a tool execution context")
	}
	if IsReadOnlyToolExecutionPolicy(e.ToolExecutionPolicy) {
		return nil, fmt.Errorf("installation is unavailable in a read-only stage")
	}
	if err := toolinput.Validate(args, map[string]string{"action": "s!", "args": "o!"}, ""); err != nil {
		return nil, err
	}
	p, _ := args["args"].(map[string]any)
	fields := map[string]string{"itemId": "s!"}
	if action == "webapp.install" {
		fields = map[string]string{"workspaceArchivePath": "s!", "expectedId": "s"}
	}
	if err := toolinput.Validate(p, fields, "args"); err != nil {
		return nil, err
	}
	form := map[string]any{"action": action, "itemId": p["itemId"], "workspaceArchivePath": p["workspaceArchivePath"], "expectedId": p["expectedId"]}
	return &ToolApproval{Fingerprint: desktopInstallFingerprint(e, tool, args), Title: tool + " / " + action, Form: form}, nil
}

func desktopInstallTool(action string) (string, bool) {
	switch action {
	case "desktop.market.installItem", "desktop.market.updateItem":
		return "desktop_market", true
	case "desktop.webapp.install":
		return "desktop_webapp", true
	default:
		return "", false
	}
}
