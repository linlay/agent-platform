package coder

import (
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type PromptTemplateData struct {
	AvailableTools          []string
	PlanStageTools          []string
	ExecuteStageTools       []string
	ExecuteToolDescriptions string
}

func RenderPromptTemplate(prompt string, values map[string]string) string {
	return strings.TrimSpace(renderTemplate(prompt, values))
}

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !IsMode(session.Mode) {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stage), "coder") {
		return ""
	}
	return RenderPromptTemplate(session.CoderSystemPrompt, PromptTemplateValues(session, req, PromptTemplateData{
		AvailableTools:    toolNames,
		PlanStageTools:    PlanningModePlanTools(),
		ExecuteStageTools: PlanningExecuteTools(toolNames),
	}))
}

func PromptTemplateValues(session contracts.QuerySession, req api.QueryRequest, data PromptTemplateData) map[string]string {
	availableTools := data.AvailableTools
	if len(availableTools) == 0 {
		availableTools = session.ToolNames
	}
	planStageTools := data.PlanStageTools
	if len(planStageTools) == 0 {
		planStageTools = PlanningModePlanTools()
	}
	executeStageTools := data.ExecuteStageTools
	if len(executeStageTools) == 0 {
		executeStageTools = PlanningExecuteTools(availableTools)
	}
	workspaceDir := firstNonBlank(
		session.RuntimeContext.LocalPaths.WorkspaceDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
		session.WorkspaceRoot,
	)
	chatDir := firstNonBlank(
		session.RuntimeContext.LocalPaths.ChatAttachmentsDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
	)
	return map[string]string{
		"agent_key":                   session.AgentKey,
		"agent_name":                  session.AgentName,
		"mode":                        session.Mode,
		"planning_mode":               fmt.Sprintf("%t", session.PlanningMode),
		"workspace_dir":               workspaceDir,
		"chat_dir":                    chatDir,
		"current_date":                time.Now().Format("2006-01-02"),
		"timezone":                    localTimezoneName(),
		"language_preference":         "中文",
		"available_tools":             strings.Join(normalizeToolNameList(availableTools), ", "),
		"plan_stage_tools":            strings.Join(normalizeToolNameList(planStageTools), ", "),
		"execute_stage_tools":         strings.Join(normalizeToolNameList(executeStageTools), ", "),
		"execute_tool_descriptions":   strings.TrimSpace(data.ExecuteToolDescriptions),
		"ask_user_question_tool_name": AskUserQuestionToolName,
		"finalize_planning_tool_name": contracts.FinalizePlanningToolName,
		"bash_tool_name":              "bash",
		"datetime_tool_name":          "datetime",
		"file_read_tool_name":         "file_read",
		"file_glob_tool_name":         "file_glob",
		"file_grep_tool_name":         "file_grep",
		"file_write_tool_name":        "file_write",
		"file_edit_tool_name":         "file_edit",
		"agent_tool_name":             contracts.InvokeAgentsToolName,
		"user_request":                req.Message,
	}
}

func renderTemplate(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
		result = strings.ReplaceAll(result, "{{ "+key+" }}", value)
	}
	return result
}

func normalizeToolNameList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func localTimezoneName() string {
	name, offset := time.Now().Zone()
	if strings.TrimSpace(name) != "" {
		return name
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	return fmt.Sprintf("UTC%s%02d:%02d", sign, hours, minutes)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
