package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf16"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopAwcpInvokeAction = "desktop.awcp.invoke"

const (
	desktopAwcpManualAction    = "desktop.awcp.manual"
	desktopAwcpGetManualMethod = "AWCP.getManual"
	desktopAwcpInvokeMethod    = "AWCP.invoke"
)

var desktopAwcpActionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

func (t *RuntimeToolExecutor) invokeDesktopAwcpManual(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := validateDesktopAwcpCall(args); err != nil {
		return desktopAwcpInvalidArgsResult(err), nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "desktop_cdp is unavailable in standalone runtime mode", nil), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("dam")
	payload, _ := args["params"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpManualAction, payload, &source, "desktop_cdp", false, execCtx)
}

func (t *RuntimeToolExecutor) invokeDesktopAwcpFromCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := validateDesktopAwcpCall(args); err != nil {
		return desktopAwcpInvalidArgsResult(err), nil
	}
	params := args["params"].(map[string]any)
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

type desktopAwcpValidationError struct {
	message string
	details map[string]any
}

func (e *desktopAwcpValidationError) Error() string {
	return e.message
}

func desktopAwcpInvalidArgsResult(err error) ToolExecutionResult {
	details := map[string]any{"executionStarted": false, "stage": "platform_parse"}
	var validationErr *desktopAwcpValidationError
	if errors.As(err, &validationErr) {
		for key, value := range validationErr.details {
			details[key] = value
		}
	}
	return desktopActionErrorResult("invalid_args", err.Error(), details)
}

func newDesktopAwcpValidationError(message string, path []string, expectedType string, actual any, present bool) error {
	details := map[string]any{
		"path":         path,
		"expectedType": expectedType,
		"actualType":   desktopAwcpJSONType(actual, present),
	}
	if len(path) > 0 {
		details["field"] = path[len(path)-1]
	}
	return &desktopAwcpValidationError{message: message, details: details}
}

func desktopAwcpJSONType(value any, present bool) string {
	if !present {
		return "missing"
	}
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func validateDesktopAwcpArgs(args map[string]any) error {
	revisionValue, revisionPresent := args["revision"]
	revision, revisionOK := revisionValue.(string)
	actionValue, actionPresent := args["action"]
	action, actionOK := actionValue.(string)
	innerArgsValue, argsPresent := args["args"]
	innerArgs, argsOK := innerArgsValue.(map[string]any)
	if !revisionOK || revision == "" || utf16Length(revision) > 128 {
		return newDesktopAwcpValidationError(
			"params.revision must be a non-empty AWCP revision of at most 128 characters.",
			[]string{"params", "revision"}, "non-empty string", revisionValue, revisionPresent,
		)
	}
	if !actionOK || utf16Length(action) > 128 || !desktopAwcpActionPattern.MatchString(action) {
		return newDesktopAwcpValidationError(
			"params.action must be a valid AWCP action name of at most 128 characters.",
			[]string{"params", "action"}, "AWCP action name string", actionValue, actionPresent,
		)
	}
	if !argsOK || innerArgs == nil {
		return newDesktopAwcpValidationError(
			fmt.Sprintf("params.args must be a JSON object; received %s.", desktopAwcpJSONType(innerArgsValue, argsPresent)),
			[]string{"params", "args"}, "object", innerArgsValue, argsPresent,
		)
	}
	if !isDesktopJSONValue(innerArgs) {
		return newDesktopAwcpValidationError(
			"params.args must contain only JSON values.",
			[]string{"params", "args"}, "object containing JSON values", innerArgsValue, true,
		)
	}
	if len(args) != 3 {
		return newDesktopAwcpValidationError(
			"AWCP.invoke params must contain exactly revision, action and args.",
			[]string{"params"}, "object with exactly revision, action and args", args, true,
		)
	}
	return nil
}

func validateDesktopAwcpManualResponse(response map[string]any, payload map[string]any) error {
	ok, okIsBool := response["ok"].(bool)
	method, methodIsString := response["method"].(string)
	if !okIsBool || !ok || !methodIsString || method != desktopAwcpGetManualMethod {
		return errors.New("AWCP manual response is invalid")
	}
	revision, valid := response["revision"].(string)
	encoded, err := json.Marshal(response)
	section, selected := payload["section"].(string)
	maximum := 64 * 1024
	if selected {
		maximum = 256 * 1024
	}
	if !valid || revision == "" || utf16Length(revision) > 128 || err != nil || len(encoded) > maximum {
		return errors.New("AWCP manual response exceeds its JSON boundary")
	}
	if selected {
		responseSection, sectionOK := response["section"].(string)
		requestRevision, revisionOK := payload["revision"].(string)
		if !sectionOK || !revisionOK || responseSection != section || revision != requestRevision {
			return errors.New("AWCP manual section identity is invalid")
		}
	}
	return nil
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

// Only the transport envelope is validated here. Reading the page manual and
// deciding which action to invoke are the model's normal tool workflow.
func validateDesktopAwcpCall(args map[string]any) error {
	for key := range args {
		if key != "method" && key != "params" {
			return newDesktopAwcpValidationError(
				fmt.Sprintf("unsupported AWCP field %q; use method and params on the current authorized page", key),
				[]string{key}, "field omitted", args[key], true,
			)
		}
	}
	paramsValue, paramsPresent := args["params"]
	params, ok := paramsValue.(map[string]any)
	method, _ := args["method"].(string)
	if strings.TrimSpace(method) == desktopAwcpGetManualMethod {
		if !paramsPresent {
			return nil
		}
		if !ok || params == nil {
			return newDesktopAwcpValidationError(
				fmt.Sprintf("AWCP.getManual params must be an object; received %s.", desktopAwcpJSONType(paramsValue, true)),
				[]string{"params"}, "object", paramsValue, true,
			)
		}
		if len(params) == 0 {
			return nil
		}
		section, sectionOK := params["section"].(string)
		revision, revisionOK := params["revision"].(string)
		if len(params) != 2 || !sectionOK || !revisionOK || revision == "" || utf16Length(revision) > 128 ||
			utf16Length(section) > 128 || !desktopAwcpActionPattern.MatchString(section) {
			return newDesktopAwcpValidationError(
				"AWCP.getManual params must be empty or contain exactly section and revision.",
				[]string{"params"}, "empty object or object with exactly section and revision", params, true,
			)
		}
		return nil
	}
	if !ok || params == nil {
		return newDesktopAwcpValidationError(
			fmt.Sprintf("AWCP.invoke params must be an object containing revision, action and args; received %s.", desktopAwcpJSONType(paramsValue, paramsPresent)),
			[]string{"params"}, "object with exactly revision, action and args", paramsValue, paramsPresent,
		)
	}
	return validateDesktopAwcpArgs(params)
}
