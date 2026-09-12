package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"unicode/utf16"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopAwcpInvokeAction = "desktop.awcp.invoke"

var desktopAwcpActionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

func (t *RuntimeToolExecutor) invokeDesktopAwcp(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := validateDesktopAwcpArgs(args); failed {
		return failure, nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_awcp_unsupported_runtime", "desktop_awcp is unavailable in standalone runtime mode", nil), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("daw")
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpInvokeAction, args, &source, "desktop_awcp", false, execCtx)
}

func validateDesktopAwcpArgs(args map[string]any) (ToolExecutionResult, bool) {
	if len(args) != 3 {
		return desktopActionErrorResult("invalid_args", "desktop_awcp arguments must contain exactly revision, action and args", nil), true
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
