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
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "webpage control requires the Desktop runtime and is unavailable in standalone mode", nil), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("dam")
	payload := desktopAwcpSurfacePayload(args)
	params, _ := args["params"].(map[string]any)
	for key, value := range params {
		payload[key] = value
	}
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpManualAction, payload, &source, "desktop_cdp", false, execCtx)
}

func (t *RuntimeToolExecutor) invokeDesktopAwcpFromCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := validateDesktopAwcpCall(args); err != nil {
		source := "params"
		if _, exists := args["paramsFile"]; exists {
			source = "paramsFile"
		}
		return desktopAwcpParameterError(err, source), nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "webpage control requires the Desktop runtime and is unavailable in standalone mode", nil), nil
	}
	parameterSource := "params"
	params, _ := args["params"].(map[string]any)
	if _, hasFile := args["paramsFile"]; hasFile {
		parameterSource = "paramsFile"
		var failure ToolExecutionResult
		var failed bool
		params, failure, failed = t.resolveDesktopCDPParams(args, execCtx)
		if failed {
			return desktopAwcpFileFailure(failure), nil
		}
	}
	if err := validateDesktopAwcpArgs(params); err != nil {
		return desktopAwcpParameterError(err, parameterSource), nil
	}
	source, err := buildDesktopActionSource(execCtx)
	if err != nil {
		return desktopActionErrorResult("invalid_execution_context", err.Error(), nil), nil
	}
	requestID := newDesktopRequestID("daw")
	payload := desktopAwcpSurfacePayload(args)
	for key, value := range params {
		payload[key] = value
	}
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpInvokeAction, payload, &source, "desktop_cdp", false, execCtx)
}

type desktopAwcpValidationError struct {
	message string
	details map[string]any
}

func (e *desktopAwcpValidationError) Error() string {
	return e.message
}

func desktopAwcpInvalidArgsResult(err error) ToolExecutionResult {
	details := map[string]any{"executionStarted": false, "stage": "platform_parse", "parameterSource": "params"}
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
	message = fmt.Sprintf("%s 位置：%s；实际类型：%s。请修正后重新调用。", message, strings.Join(path, "."), details["actualType"])
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
	for _, key := range []string{"method", "surfaceId", "params", "paramsFile"} {
		if _, exists := args[key]; exists {
			return newDesktopAwcpValidationError("参数对象只包含 revision、action、args；不要包装整条调用，method 和 surfaceId 留在外层。", []string{"params"}, "object with exactly revision, action and args", args, true)
		}
	}
	revisionValue, revisionPresent := args["revision"]
	revision, revisionOK := revisionValue.(string)
	actionValue, actionPresent := args["action"]
	action, actionOK := actionValue.(string)
	innerArgsValue, argsPresent := args["args"]
	innerArgs, argsOK := innerArgsValue.(map[string]any)
	if !revisionOK || revision == "" || utf16Length(revision) > 128 {
		return newDesktopAwcpValidationError(
			"params.revision 必须填写手册返回的非空 revision，最多 128 个 UTF-16 字符。",
			[]string{"params", "revision"}, "non-empty string", revisionValue, revisionPresent,
		)
	}
	if !actionOK || utf16Length(action) > 128 || !desktopAwcpActionPattern.MatchString(action) {
		return newDesktopAwcpValidationError(
			"params.action 必须填写手册中的动作名称，最多 128 个 UTF-16 字符，仅允许小写字母、数字及分段的连字符和点。",
			[]string{"params", "action"}, "AWCP action name string", actionValue, actionPresent,
		)
	}
	if !argsOK || innerArgs == nil {
		return newDesktopAwcpValidationError(
			"args 必须是原生 JSON 对象；无参数动作传 args: {}，有参数动作按手册填写对象，不要传空字符串或字符串 {}",
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
func desktopAwcpSurfacePayload(args map[string]any) map[string]any {
	payload := map[string]any{}
	if id := strings.TrimSpace(stringArg(args, "surfaceId")); id != "" {
		payload["surfaceId"] = id
	}
	return payload
}

func validateDesktopAwcpCall(args map[string]any) error {
	if raw, present := args["surfaceId"]; present {
		id, ok := raw.(string)
		if !ok || strings.TrimSpace(id) == "" {
			return newDesktopAwcpValidationError("surfaceId 必须为非空页面标识。", []string{"surfaceId"}, "non-empty string", raw, true)
		}
	}
	method, _ := args["method"].(string)
	invoke := strings.TrimSpace(method) == desktopAwcpInvokeMethod
	for key := range args {
		if key != "method" && key != "params" && key != "surfaceId" && !(invoke && key == "paramsFile") {
			return newDesktopAwcpValidationError(
				"不支持的 AWCP 外层字段；保留 method、参数来源和可选 surfaceId，不接受 requestId。",
				[]string{key}, "field omitted", args[key], true,
			)
		}
	}
	paramsValue, paramsPresent := args["params"]
	params, ok := paramsValue.(map[string]any)
	if invoke {
		rawFile, hasFile := args["paramsFile"]
		if hasFile && paramsPresent {
			return newDesktopAwcpValidationError("params 与 paramsFile 必须二选一；删除其中一个来源，不合并。", []string{"paramsFile"}, "exclusive parameter source", rawFile, true)
		}
		if hasFile {
			path, valid := rawFile.(string)
			if !valid || strings.TrimSpace(path) == "" {
				return newDesktopAwcpValidationError("paramsFile 必须填写 UTF-8 JSON 参数文件的非空路径。", []string{"paramsFile"}, "non-empty path string", rawFile, true)
			}
			return nil
		}
		if !paramsPresent {
			return newDesktopAwcpValidationError("缺少参数来源；提供 params 对象或 paramsFile 文件路径，必须二选一。", []string{"params"}, "object or paramsFile path", nil, false)
		}
	}
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

// Parameter source is diagnostic only; it never becomes part of the wire envelope.
func desktopAwcpParameterError(err error, source string) ToolExecutionResult {
	var validationErr *desktopAwcpValidationError
	if errors.As(err, &validationErr) {
		validationErr.details["parameterSource"] = source
		path, _ := validationErr.details["path"].([]string)
		if source == "paramsFile" && len(path) > 0 && path[0] == "params" {
			validationErr.message += " 请修正 paramsFile 文件内容后重新调用。"
		}
	}
	return desktopAwcpInvalidArgsResult(err)
}

func desktopAwcpFileFailure(result ToolExecutionResult) ToolExecutionResult {
	// Preserve approval fingerprints/rule keys and existing file error codes.
	payload := result.Structured
	details, _ := payload["details"].(map[string]any)
	if details == nil {
		details = map[string]any{}
		payload["details"] = details
	}
	details["parameterSource"] = "paramsFile"
	details["executionStarted"] = false
	details["stage"] = "platform_parse"
	if _, exists := details["path"]; !exists {
		details["path"] = []string{"paramsFile"}
		details["expectedType"] = "readable UTF-8 JSON object file"
		details["actualType"] = "string"
	}
	if errorPayload, ok := payload["error"].(map[string]any); ok {
		errorPayload["message"] = fmt.Sprint(errorPayload["message"]) + " 请修正 paramsFile 文件或路径后重新调用。"
	} else if result.Error != "desktop_cdp_params_file_approval_required" {
		payload["message"] = fmt.Sprint(payload["message"]) + " 请修正 paramsFile 文件或路径后重新调用。"
	}
	result.Output = structuredResultWithExit(payload, result.ExitCode).Output
	return result
}
