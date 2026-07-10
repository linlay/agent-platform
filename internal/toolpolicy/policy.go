package toolpolicy

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const DisabledErrorCode = "btw_tool_disabled"

var builtinReadOnlyTools = map[string]struct{}{
	"datetime":               {},
	"regex":                  {},
	"file_read":              {},
	"file_grep":              {},
	"file_glob":              {},
	"web_fetch":              {},
	"vision_recognize":       {},
	"memory_search":          {},
	"memory_read":            {},
	"memory_timeline":        {},
	"kbase_search":           {},
	"kbase_read":             {},
	"kbase_files":            {},
	"kbase_status":           {},
	"session_search":         {},
	"_session_search_":       {},
	"skill_candidate_list":   {},
	"_skill_candidate_list_": {},
	"plan_get_tasks":         {},
}

var alwaysDeniedTools = map[string]struct{}{
	"bash":                    {},
	"bash_sandbox":            {},
	"_sandbox_bash_":          {},
	"simple-bash":             {},
	"file_write":              {},
	"file_edit":               {},
	"memory_write":            {},
	"memory_update":           {},
	"memory_forget":           {},
	"memory_promote":          {},
	"memory_consolidate":      {},
	"kbase_refresh":           {},
	"plan_add_tasks":          {},
	"plan_update_task":        {},
	"finalize_planning":       {},
	"agent_invoke":            {},
	"artifact_publish":        {},
	"image_generate":          {},
	"desktop_action":          {},
	"desktop_cdp":             {},
	"ask_user_question":       {},
	"skill_candidate_write":   {},
	"_skill_candidate_write_": {},
}

func AllowsReadOnly(def api.ToolDetailResponse, found bool) bool {
	if !found {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(stringMeta(def.Meta, "kind")))
	if kind == "frontend" || kind == "action" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(def.Name))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(def.Key))
	}
	if _, denied := alwaysDeniedTools[name]; denied {
		return false
	}
	if _, ok := builtinReadOnlyTools[name]; ok {
		sourceCategory := strings.ToLower(strings.TrimSpace(stringMeta(def.Meta, "sourceCategory")))
		return sourceCategory == "platform"
	}
	return boolMeta(def.Meta, "readOnly")
}

func DisabledResult(toolName string) contracts.ToolExecutionResult {
	payload := map[string]any{
		"error":    DisabledErrorCode,
		"toolName": strings.TrimSpace(toolName),
		"policy":   contracts.ToolExecutionPolicyReadOnly,
		"message":  "tool execution is disabled in BTW read-only mode",
	}
	encoded, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{
		Output:     string(encoded),
		Structured: payload,
		Error:      DisabledErrorCode,
		ExitCode:   -1,
	}
}

func stringMeta(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

func boolMeta(meta map[string]any, key string) bool {
	value, _ := meta[key].(bool)
	return value
}
