package coder

import (
	"strings"

	"agent-platform/internal/contracts"
)

const AskUserQuestionToolName = "ask_user_question"

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

func RuntimeToolNamesForStage(mode string, stage string, toolNames []string) []string {
	out := append([]string(nil), toolNames...)
	if !strings.EqualFold(strings.TrimSpace(mode), "CODER") {
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
