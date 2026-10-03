package tools

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"context"
	"fmt"
)

func (t *RuntimeToolExecutor) invokeDesktopDomain(ctx context.Context, tool string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	fail := func(code, message string) (ToolExecutionResult, error) {
		return desktopActionErrorResult(code, message, map[string]any{"stage": "arguments", "executionState": "not_started", "recovery": map[string]any{"strategy": "fix_input"}}), nil
	}
	for key := range args {
		if key != "action" && key != "args" {
			return fail("invalid_args", fmt.Sprintf("unknown field %s; expected action and args", key))
		}
	}
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return fail("invalid_args", "action must be a non-empty string")
	}
	_, ok = connector.LookupControlAction(tool, action)
	if !ok {
		if owner := connector.ControlActionOwner(action); owner != "" {
			return fail("action_tool_mismatch", "action belongs to "+owner)
		}
		return fail("unknown_action", "unsupported action for "+tool)
	}
	if raw, exists := args["args"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return fail("invalid_args", "args must be an object")
		}
	}
	if execCtx != nil && IsReadOnlyToolExecutionPolicy(execCtx.ToolExecutionPolicy) {
		return fail("stage_forbidden", "action is unavailable in a read-only stage")
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return fail("desktop_unsupported_runtime", "Desktop domain tools require Desktop runtime")
	}
	// The existing transport retains its structured error codes and confirmation policy.
	return t.dispatchDesktopAction(ctx, "desktop."+action, args, execCtx)
}
