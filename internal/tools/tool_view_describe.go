package tools

import (
	"encoding/json"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/view"
)

// invokeViewDescribe discloses one display view of the caller's frozen
// mounts. Keys outside those mounts are not found, so the tool reveals
// nothing the Agent could not already render.
func (r *ToolRouter) invokeViewDescribe(args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	for key := range args {
		if key != "key" {
			return modelToolError("invalid_arguments", "unknown field "+key, nil)
		}
	}
	key, ok := args["key"].(string)
	if !ok || strings.TrimSpace(key) == "" {
		return modelToolError("invalid_arguments", "key must be a non-empty string", nil)
	}
	if execCtx == nil {
		return modelToolError("view_not_found", "no display view "+key, nil)
	}
	description, err := view.Describe(execCtx.Session.ViewMounts, key)
	if err != nil {
		return modelToolError("view_not_found", "no display view "+strings.TrimSpace(key)+"; use a key from the view catalog", nil)
	}
	data, err := json.Marshal(description)
	var payload map[string]any
	if err == nil {
		err = json.Unmarshal(data, &payload)
	}
	if err != nil {
		return modelToolError("view_unavailable", "view "+description.Key+" cannot be described", nil)
	}
	return structuredResult(payload)
}
