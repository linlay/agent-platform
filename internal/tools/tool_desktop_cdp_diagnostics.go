package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	. "agent-platform/internal/contracts"
)

// Desktop sends a CDP response envelope in error-frame data. Only copy public,
// bounded diagnostics: never forward arbitrary params, URLs or host identities.
func appendDesktopCDPDiagnostics(out map[string]any, data json.RawMessage) {
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil {
		return
	}
	copyDesktopCDPDiagnosticFields(out, envelope)
	details, _ := envelope["details"].(map[string]any)
	copyDesktopCDPDiagnosticFields(out, details)
	failure, _ := envelope["error"].(map[string]any)
	if code, ok := failure["code"].(string); ok && len(code) <= 128 {
		out["clientErrorType"] = code
	}
	details, _ = failure["details"].(map[string]any)
	copyDesktopCDPDiagnosticFields(out, details)
}

func copyDesktopCDPDiagnosticFields(out, in map[string]any) {
	for _, key := range []string{"method", "targetId", "surfaceId", "reason", "recovery"} {
		if value, ok := in[key].(string); ok && len(value) <= 2048 {
			out[key] = value
		}
	}
	for _, key := range []string{"executed", "retryable"} {
		if value, ok := in[key].(bool); ok {
			out[key] = value
		}
	}
	for _, key := range []string{"timeoutMs", "elapsedMs"} {
		if value, ok := in[key].(float64); ok {
			out[key] = value
		}
	}
	raw, _ := in["issues"].([]any)
	issues := make([]any, 0, len(raw))
	for index, value := range raw {
		if index >= 32 {
			break
		}
		issue, ok := value.(map[string]any)
		if !ok {
			continue
		}
		clean := map[string]any{}
		for _, key := range []string{"path", "expected", "actualType"} {
			if value, ok := issue[key].(string); ok && len(value) <= 512 {
				clean[key] = value
			}
		}
		if len(clean) != 3 {
			continue
		}
		switch value := issue["actualValue"].(type) {
		case bool, float64:
			clean["actualValue"] = value
		case string:
			if len(value) <= 80 {
				clean["actualValue"] = value
			}
		}
		issues = append(issues, clean)
	}
	if len(issues) > 0 {
		out["issues"] = issues
	}
}

func desktopCDPEvaluationFailure(response, structured map[string]any) (ToolExecutionResult, bool) {
	if response["method"] != "Runtime.evaluate" {
		return ToolExecutionResult{}, false
	}
	result, _ := response["result"].(map[string]any)
	exception, ok := result["exceptionDetails"].(map[string]any)
	if !ok {
		return ToolExecutionResult{}, false
	}
	remote, _ := exception["exception"].(map[string]any)
	description, _ := remote["description"].(string)
	text, _ := exception["text"].(string)
	message := firstDesktopActionMessage(description, firstDesktopActionMessage(text, "JavaScript evaluation failed"))
	// CDP positions are zero-based; label them explicitly instead of changing them.
	if line, ok := exception["lineNumber"].(float64); ok {
		message += fmt.Sprintf(" (CDP zero-based line %g", line)
		if column, ok := exception["columnNumber"].(float64); ok {
			message += fmt.Sprintf(", column %g", column)
		}
		message += ")"
	}
	structured["ok"] = false
	structured["error"] = map[string]any{"code": "desktop_cdp_evaluation_failed", "message": strings.TrimSpace(message)}
	structured["details"] = map[string]any{
		"method": "Runtime.evaluate", "retryable": false,
		"recovery": "Inspect exceptionDetails and correct the expression before retrying. CDP transport succeeded but JavaScript evaluation failed; earlier script side effects may have occurred. A successful evaluation alone does not verify a form update.",
	}
	failure := structuredResultWithExit(structured, -1)
	failure.Error = "desktop_cdp_evaluation_failed"
	return failure, true
}
