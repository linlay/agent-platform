package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf16"

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
	if len(args) != 1 {
		return desktopActionErrorResult("invalid_args", "AWCP.getSnapshot accepts only method", nil), nil
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
	if len(args) != 2 {
		return desktopActionErrorResult("invalid_args", "AWCP.invoke arguments must contain exactly method and params", nil), nil
	}
	params, ok := args["params"].(map[string]any)
	if !ok || params == nil {
		return desktopActionErrorResult("invalid_args", "AWCP.invoke params must be an object", map[string]any{"field": "params"}), nil
	}
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
		return desktopActionErrorResult("invalid_args", "AWCP.invoke params must contain exactly revision, action and args", nil), true
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
	revision, revisionIsString := response["revision"].(string)
	actions, actionsIsArray := response["actions"].([]any)
	if !okIsBool || !ok || !methodIsString || method != desktopAwcpGetSnapshotMethod ||
		!revisionIsString || revision == "" || utf16Length(revision) > 128 ||
		!actionsIsArray || len(actions) > 128 {
		return errors.New("AWCP snapshot response is invalid")
	}
	snapshot := map[string]any{"revision": revision, "actions": actions}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > 256*1024 {
		return errors.New("AWCP snapshot response exceeds its JSON boundary")
	}
	previous := ""
	for _, rawAction := range actions {
		descriptor, ok := rawAction.(map[string]any)
		if !ok || (len(descriptor) != 3 && len(descriptor) != 4) {
			return errors.New("AWCP snapshot action descriptor is invalid")
		}
		if _, ok := descriptor["action"]; !ok {
			return errors.New("AWCP snapshot action descriptor is invalid")
		}
		if _, ok := descriptor["description"]; !ok {
			return errors.New("AWCP snapshot action descriptor is invalid")
		}
		if _, ok := descriptor["inputSchema"]; !ok {
			return errors.New("AWCP snapshot action descriptor is invalid")
		}
		if len(descriptor) == 4 {
			if _, ok := descriptor["outputSchema"]; !ok {
				return errors.New("AWCP snapshot action descriptor is invalid")
			}
		}
		action, actionOK := descriptor["action"].(string)
		description, descriptionOK := descriptor["description"].(string)
		inputSchema, inputOK := descriptor["inputSchema"].(map[string]any)
		if !actionOK || action == "" || utf16Length(action) > 128 ||
			!desktopAwcpActionPattern.MatchString(action) || action <= previous ||
			!descriptionOK || strings.TrimSpace(description) == "" || utf16Length(description) > 2048 ||
			!inputOK || inputSchema == nil || !validDesktopAwcpJSONTree(inputSchema, 0, 20) {
			return errors.New("AWCP snapshot action descriptor is invalid")
		}
		if outputSchema, present := descriptor["outputSchema"]; present {
			output, outputOK := outputSchema.(map[string]any)
			if !outputOK || output == nil || !validDesktopAwcpJSONTree(output, 0, 20) {
				return errors.New("AWCP snapshot output schema is invalid")
			}
		}
		previous = action
	}
	return nil
}

func validDesktopAwcpJSONTree(value any, depth, maximumDepth int) bool {
	if depth > maximumDepth {
		return false
	}
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
			if !validDesktopAwcpJSONTree(item, depth+1, maximumDepth) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !validDesktopAwcpJSONTree(item, depth+1, maximumDepth) {
				return false
			}
		}
		return true
	default:
		return false
	}
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
