package tools

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// desktopControlReviewAction covers only actions whose trusted Platform calls
// are exempt from Desktop confirmation. Market and host lifecycle stay there.
func desktopControlReviewAction(tool, action string) bool {
	if _, ok := connector.LookupControlAction(tool, action); !ok {
		return false
	}
	switch action {
	case "theme.set", "locale.set", "skin.import", "skin.set", "skin.remove",
		"pet.show", "pet.hide", "pet.set", "pet.import", "copilot.setPagePreference",
		"website.add", "website.update", "website.remove",
		"kanban.createIssue", "kanban.updateIssue", "kanban.deleteIssue", "kanban.moveIssue",
		"webapp.start", "webapp.stop", "webapp.restart", "webapp.open", "webapp.updatePreferences", "webapp.unpublish",
		"web.exportArtifact", "runtime.diagnostics":
		return true
	}
	return false
}

func desktopControlFingerprint(e *ExecutionContext, tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	digest := sha256.Sum256(raw)
	return ToolApprovalFingerprint(e, tool, AnyStringNode(args["action"]), hex.EncodeToString(digest[:]))
}

// PrepareToolApproval reviews the requested action before Desktop execution.
// Desktop still owns argument validation and actual resource state.
func (t *RuntimeToolExecutor) PrepareToolApproval(ctx context.Context, tool string, args map[string]any, e *ExecutionContext) (*ToolApproval, error) {
	action := AnyStringNode(args["action"])
	if !desktopControlReviewAction(tool, action) {
		return nil, nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return nil, fmt.Errorf("Desktop actions require Desktop runtime")
	}
	if e == nil || e.CurrentToolID == "" {
		return nil, fmt.Errorf("action review requires a tool execution context")
	}
	if IsReadOnlyToolExecutionPolicy(e.ToolExecutionPolicy) {
		return nil, fmt.Errorf("action is unavailable in a read-only stage")
	}
	if err := toolinput.Validate(args, map[string]string{"action": "s!", "args": "o"}, ""); err != nil {
		return nil, err
	}
	params, _ := args["args"].(map[string]any)
	for _, key := range desktopActionReservedArgFields {
		if _, exists := params[key]; exists {
			return nil, fmt.Errorf("args.%s is reserved", key)
		}
	}
	form := map[string]any{"action": action, "args": CloneMap(params)}
	level := e.AccessLevel
	if level == "" {
		level = e.Session.AccessLevel
	}
	if level != AccessLevelAutoApprove && level != AccessLevelFullAccess {
		for key, value := range t.desktopReviewContext(ctx, action, params, e) {
			form[key] = value
		}
	}
	if action == "runtime.diagnostics" {
		// Describe categories only; never fetch sensitive values to prepare a review.
		form["categories"] = []string{"device", "host paths", "runtime versions", "SSO credential summary", "internal services"}
	}
	return &ToolApproval{Fingerprint: desktopControlFingerprint(e, tool, args), Title: tool + " / " + action, Form: form, AllowAutoApprove: true}, nil
}
