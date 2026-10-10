// Package planmode implements planningMode for native Agents of any mode: the
// planning Run that produces a plan for confirmation, and the tool rules of
// the separate Run that executes a confirmed plan.
package planmode

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const (
	// PlanningStage and PlanningFeedbackStage label the model loops of a
	// planning Run. They are internal labels, not a Run-level phase switch.
	PlanningStage         = "planning"
	PlanningFeedbackStage = "planning-feedback"

	AskUserQuestionToolName = "ask_user_question"

	// ApproveContinuationParam marks the Run started from a confirmed plan. The
	// persisted name predates planmode and is kept so existing Runs resume.
	ApproveContinuationParam = "_coderPlanningApproveContinuation"
)

// IsPlanningStage reports whether a stage label belongs to a planning Run.
func IsPlanningStage(stage string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(stage)), PlanningStage)
}

// PlanningTools is the tool set of a planning Run: the Agent's effective tools
// without the configured planning exclusions, plus the finalize_planning
// lifecycle tool. Definitions let an exclusion match a tool listed by its key.
func PlanningTools(session contracts.QuerySession, defs []api.ToolDetailResponse) []string {
	tools := removeToolNames(session.ToolNames, excludedToolNames(session.PlanningExcludeTools, defs)...)
	tools = removeToolNames(tools, contracts.FinalizePlanningToolName, "agent_delegate")
	return append(tools, contracts.FinalizePlanningToolName)
}

// ConfirmedPlanTools is the tool set of a Run started from a confirmed plan:
// the Agent's effective tools without the configured execution exclusions.
func ConfirmedPlanTools(session contracts.QuerySession, defs []api.ToolDetailResponse) []string {
	tools := removeToolNames(session.ToolNames, excludedToolNames(session.PlanExecuteExcludeTools, defs)...)
	return removeToolNames(tools, contracts.FinalizePlanningToolName)
}

func excludedToolNames(exclude []string, defs []api.ToolDetailResponse) []string {
	if len(exclude) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(exclude))
	out := make([]string, 0, len(exclude))
	add := func(name string) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			return
		}
		if _, seen := blocked[key]; !seen {
			blocked[key] = struct{}{}
			out = append(out, name)
		}
	}
	for _, name := range exclude {
		add(name)
	}
	for _, def := range defs {
		_, byName := blocked[strings.ToLower(strings.TrimSpace(def.Name))]
		_, byKey := blocked[strings.ToLower(strings.TrimSpace(def.Key))]
		if byName || byKey {
			add(def.Name)
			add(def.Key)
		}
	}
	return out
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

// IsConfirmedPlanRun reports whether request params mark a Run started from a
// confirmed plan.
func IsConfirmedPlanRun(params map[string]any) bool {
	if len(params) == 0 {
		return false
	}
	value, ok := params[ApproveContinuationParam]
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

func MarkConfirmedPlanRun(params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params[ApproveContinuationParam] = true
	return params
}

// ConfirmedPlanPrompt is the user message that starts the execution Run.
func ConfirmedPlanPrompt(originalRequest string, planningMarkdown string) string {
	return "Execute the confirmed plan.\n\nOriginal request:\n" + originalRequest + "\n\nConfirmed planning:\n" + planningMarkdown +
		"\n\nThe user has already approved this plan. Carry it out without asking the user further questions; if it turns out to be unsafe or impossible, stop and explain instead of guessing."
}
