package tools

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
	"context"
	"errors"
)

func (t *RuntimeToolExecutor) invokeDesktopDomain(ctx context.Context, tool string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	fail := func(code, message string) (ToolExecutionResult, error) {
		return desktopActionErrorResult(code, message, map[string]any{"stage": "arguments", "executionState": "not_started", "recovery": map[string]any{"strategy": "fix_input"}}), nil
	}
	inputFail := func(err error) (ToolExecutionResult, error) {
		var input *toolinput.Error
		if errors.As(err, &input) {
			details := input.Details()
			details["stage"] = "arguments"
			details["executionState"] = "not_started"
			return desktopActionErrorResult("invalid_args", input.Error(), details), nil
		}
		return fail("invalid_args", err.Error())
	}
	if err := toolinput.Validate(args, map[string]string{"action": "s!", "args": "o"}, ""); err != nil {
		return inputFail(err)
	}
	action := args["action"].(string)
	if _, ok := connector.LookupControlAction(tool, action); !ok {
		var allowed []string
		for _, a := range connector.ControlActions() {
			if a.Tool == tool {
				allowed = append(allowed, a.Action)
			}
		}
		err := toolinput.Enum("action", action, allowed)
		if owner := connector.ControlActionOwner(action); owner != "" {
			return fail("action_tool_mismatch", "Use tool "+owner+" for this action; choose an action belonging to the selected tool.")
		}
		details := err.(*toolinput.Error).Details()
		details["stage"] = "arguments"
		details["executionState"] = "not_started"
		return desktopActionErrorResult("unknown_action", err.Error(), details), nil
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
