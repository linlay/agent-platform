package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"unicode/utf16"

	"agent-platform/internal/awcp"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopAwcpInvokeAction = "desktop.awcp.invoke"

const (
	desktopAwcpSnapshotAction    = "desktop.awcp.snapshot"
	desktopAwcpGetSnapshotMethod = "AWCP.getSnapshot"
	desktopAwcpInvokeMethod      = "AWCP.invoke"
)

var desktopAwcpActionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

func (t *RuntimeToolExecutor) invokeDesktopAwcpSnapshot(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := ValidateDesktopAwcpCall(args); err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), map[string]any{"executionStarted": false, "stage": "platform_parse"}), nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "desktop_cdp is unavailable in standalone runtime mode", nil), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("das")
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpSnapshotAction, map[string]any{}, &source, "desktop_cdp", false, execCtx)
}

func (t *RuntimeToolExecutor) invokeDesktopAwcpFromCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := ValidateDesktopAwcpCall(args); err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), map[string]any{"executionStarted": false, "stage": "platform_parse"}), nil
	}
	if execCtx == nil || execCtx.DesktopAwcpRevision == "" {
		return desktopActionErrorResult("awcp_request_binding_missing", "AWCP.invoke requires the model request's trusted snapshot binding", map[string]any{"executionStarted": false, "stage": "platform_parse"}), nil
	}
	action, input, _ := DesktopAwcpInvocation(args)
	params := map[string]any{"revision": execCtx.DesktopAwcpRevision, "action": action, "args": input}
	if failure, failed := validateDesktopAwcpArgs(params); failed {
		return failure, nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "desktop_cdp is unavailable in standalone runtime mode", nil), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("daw")
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpInvokeAction, params, &source, "desktop_cdp", false, execCtx)
}

func validateDesktopAwcpArgs(args map[string]any) (ToolExecutionResult, bool) {
	if len(args) != 3 {
		return desktopActionErrorResult("invalid_args", "internal AWCP payload must contain exactly revision, action and args", nil), true
	}
	revision, revisionOK := args["revision"].(string)
	action, actionOK := args["action"].(string)
	innerArgs, argsOK := args["args"].(map[string]any)
	if !revisionOK || revision == "" || utf16Length(revision) > 128 {
		return desktopActionErrorResult("invalid_args", "revision must be a non-empty AWCP revision of at most 128 characters", map[string]any{"field": "revision"}), true
	}
	if !actionOK || utf16Length(action) > 128 || !desktopAwcpActionPattern.MatchString(action) {
		return desktopActionErrorResult("invalid_args", "action is not a valid AWCP action name", map[string]any{"field": "action"}), true
	}
	if !argsOK || innerArgs == nil || !isDesktopJSONValue(innerArgs) {
		return desktopActionErrorResult("invalid_args", "args must be a JSON object", map[string]any{"field": "args"}), true
	}
	return ToolExecutionResult{}, false
}

func validateDesktopAwcpSnapshotResponse(response map[string]any) error {
	if len(response) != 4 {
		return errors.New("AWCP snapshot response fields are invalid")
	}
	ok, okIsBool := response["ok"].(bool)
	method, methodIsString := response["method"].(string)
	if !okIsBool || !ok || !methodIsString || method != desktopAwcpGetSnapshotMethod {
		return errors.New("AWCP snapshot response is invalid")
	}
	_, _, err := awcp.ParseSnapshot(map[string]any{"revision": response["revision"], "actions": response["actions"]})
	return err
}

func validateDesktopAwcpResponse(response map[string]any, requestID string, payload map[string]any) (bool, error) {
	ok, okIsBool := response["ok"].(bool)
	responseRequestID, requestIDIsString := response["requestId"].(string)
	responseAction, actionIsString := response["action"].(string)
	expectedAction, _ := payload["action"].(string)
	if !okIsBool || !requestIDIsString || !actionIsString || responseRequestID != requestID || responseAction != expectedAction {
		return false, errors.New("AWCP response identity or discriminant is invalid")
	}
	if ok {
		if len(response) != 4 {
			return false, errors.New("AWCP success response fields are invalid")
		}
		if _, exists := response["result"]; !exists {
			return false, errors.New("AWCP success response result is missing")
		}
		return false, nil
	}
	if len(response) != 4 {
		return false, errors.New("AWCP failure response fields are invalid")
	}
	if _, exists := response["error"].(map[string]any); !exists {
		return false, errors.New("AWCP failure response error is invalid")
	}
	return true, nil
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func isDesktopJSONValue(value any) bool {
	if _, err := json.Marshal(value); err != nil {
		return false
	}
	return isDesktopJSONValueTree(value)
}

func isDesktopJSONValueTree(value any) bool {
	switch typed := value.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case []any:
		for _, item := range typed {
			if !isDesktopJSONValueTree(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !isDesktopJSONValueTree(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
