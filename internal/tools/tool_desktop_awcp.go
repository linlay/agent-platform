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
	desktopAwcpSnapshotAction    = "desktop.awcp.snapshot"
	desktopAwcpGetSnapshotMethod = "AWCP.getSnapshot"
	desktopAwcpInvokeMethod      = "AWCP.invoke"
)

var desktopAwcpActionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

func (t *RuntimeToolExecutor) invokeDesktopAwcpSnapshot(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := validateDesktopAwcpCall(args); err != nil {
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
	result, err := t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpSnapshotAction, desktopAwcpSurfacePayload(args), &source, "desktop_cdp", false, execCtx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		return result, err
	}
	// The page is the documentation source. Reveal only the index until the
	// model asks for one action; never install page schemas into model tools.
	response := result.Structured["response"].(map[string]any)
	params, _ := args["params"].(map[string]any)
	selected, _ := params["action"].(string)
	entries := make([]any, 0)
	for _, raw := range response["actions"].([]any) {
		descriptor := raw.(map[string]any)
		if selected == "" {
			entries = append(entries, map[string]any{"action": descriptor["action"], "description": descriptor["description"]})
		} else if descriptor["action"] == selected {
			entries = append(entries, descriptor)
		}
	}
	if selected != "" && len(entries) == 0 {
		return desktopActionErrorResult("awcp_action_not_found", "The requested action is absent from the current page manual; read the index again.", map[string]any{"executionStarted": false}), nil
	}
	return structuredResult(map[string]any{"transport": "reverse-websocket", "response": map[string]any{
		"ok": true, "method": desktopAwcpGetSnapshotMethod, "revision": response["revision"], "actions": entries,
	}}), nil
}

func (t *RuntimeToolExecutor) invokeDesktopAwcpFromCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := validateDesktopAwcpCall(args); err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), map[string]any{"executionStarted": false, "stage": "platform_parse"}), nil
	}
	params := args["params"].(map[string]any)
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
	payload := desktopAwcpSurfacePayload(args)
	for key, value := range params {
		payload[key] = value
	}
	return t.invokeDesktopClientRequest(ctx, requestID, desktopAwcpInvokeAction, payload, &source, "desktop_cdp", false, execCtx)
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
	if !okIsBool || !ok || !methodIsString || method != desktopAwcpGetSnapshotMethod {
		return errors.New("AWCP snapshot response is invalid")
	}
	revision, valid := response["revision"].(string)
	actions, array := response["actions"].([]any)
	encoded, err := json.Marshal(response)
	if !valid || revision == "" || utf16Length(revision) > 128 || !array || len(actions) > 128 || err != nil || len(encoded) > 256*1024 {
		return errors.New("AWCP snapshot exceeds its JSON boundary")
	}
	previous := ""
	for _, raw := range actions {
		descriptor, ok := raw.(map[string]any)
		name, _ := descriptor["action"].(string)
		description, _ := descriptor["description"].(string)
		if !ok || !desktopAwcpActionPattern.MatchString(name) || utf16Length(name) > 128 || name <= previous || strings.TrimSpace(description) == "" || utf16Length(description) > 2048 {
			return errors.New("AWCP snapshot action index is invalid")
		}
		previous = name
	}
	// Business schemas, examples and manual content are page-owned data.
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
			return fmt.Errorf("surfaceId must be a non-empty string")
		}
	}
	for key := range args {
		if key != "method" && key != "params" && key != "surfaceId" {
			return fmt.Errorf("unsupported AWCP field %q; use method and params on the current authorized page", key)
		}
	}
	params, ok := args["params"].(map[string]any)
	method, _ := args["method"].(string)
	if strings.TrimSpace(method) == desktopAwcpGetSnapshotMethod {
		if _, present := args["params"]; !present {
			return nil
		}
		if !ok || params == nil {
			return errors.New("AWCP.getSnapshot params must be an object")
		}
		if len(params) == 0 {
			return nil
		}
		action, valid := params["action"].(string)
		if len(params) != 1 || !valid || utf16Length(action) > 128 || !desktopAwcpActionPattern.MatchString(action) {
			return errors.New("AWCP.getSnapshot accepts only an optional action name to read its manual")
		}
		return nil
	}
	if !ok || params == nil {
		return errors.New("AWCP.invoke params must contain revision, action and args")
	}
	if failure, failed := validateDesktopAwcpArgs(params); failed {
		return errors.New(failure.Output)
	}
	return nil
}
