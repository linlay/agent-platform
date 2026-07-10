package coder

import (
	"strings"

	"agent-platform/internal/contracts"
)

const AskUserQuestionToolName = "ask_user_question"

const PlanApproveContinuationParam = "_coderPlanApproveContinuation"

var planningModePlanTools = []string{
	"file_read",
	"file_glob",
	"file_grep",
	"datetime",
	"regex",
	"vision_recognize",
	AskUserQuestionToolName,
	contracts.FinalizePlanningToolName,
}

func PlanningModePlanTools() []string {
	return append([]string(nil), planningModePlanTools...)
}

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "CODER")
}

func IsACPBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) != ""
}

func IsNativeBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) == ""
}

func PlanningModeEnabled(mode string, requested bool) bool {
	return requested && IsMode(mode)
}

func IsPlanApproveContinuationParams(params map[string]any) bool {
	if len(params) == 0 {
		return false
	}
	value, ok := params[PlanApproveContinuationParam]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func MarkPlanApproveContinuationParams(params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params[PlanApproveContinuationParam] = true
	return params
}

func PlanApproveExecutePrompt(originalRequest string, planMarkdown string) string {
	return "Execute the confirmed CODER plan.\n\nOriginal request:\n" + originalRequest + "\n\nConfirmed plan:\n" + planMarkdown
}

func SystemPromptForMode(mode string, prompt string) string {
	if !IsMode(mode) {
		return ""
	}
	return strings.TrimSpace(prompt)
}

func RuntimeToolNamesForAgent(mode string, acpBridgeID string, stage string, toolNames []string) []string {
	if !IsNativeBackend(mode, acpBridgeID) {
		return append([]string(nil), toolNames...)
	}
	return RuntimeToolNamesForStage(mode, stage, toolNames)
}

func RuntimeToolNamesForStage(mode string, stage string, toolNames []string) []string {
	out := append([]string(nil), toolNames...)
	if !IsMode(mode) {
		return out
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if stage == "coder" || strings.HasPrefix(stage, "coder-execute") {
		return contracts.AppendPlanTaskToolNames(out)
	}
	return out
}

func PlanningExecuteTools(toolNames []string) []string {
	tools := contracts.AppendPlanTaskToolNames(toolNames)
	return removeToolNames(tools, contracts.FinalizePlanningToolName, AskUserQuestionToolName)
}

func IsPlanningOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case contracts.FinalizePlanningToolName, AskUserQuestionToolName:
		return true
	default:
		return false
	}
}

func removeToolNames(base []string, names ...string) []string {
	blocked := map[string]struct{}{}
	for _, name := range names {
		if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
			blocked[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(base))
	for _, name := range base {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		out = append(out, name)
	}
	return out
}
