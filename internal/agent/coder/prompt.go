package coder

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
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
	return agentcontract.RenderPromptTemplate(prompt, values)
}

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !IsMode(session.Mode) {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	return RenderPromptTemplate(session.ModeSystemPrompt, PromptTemplateValues(session, req, PromptTemplateData{
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
	workspaceDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.WorkspaceDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
		session.WorkspaceRoot,
	)
	chatDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.ChatAttachmentsDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
	)
	values := agentcontract.CommonPromptValues(agentcontract.PromptContext{
		AgentKey: session.AgentKey, AgentName: session.AgentName, Mode: session.Mode,
		PlanningMode: session.PlanningMode, WorkspaceDir: workspaceDir, ChatDir: chatDir,
		AvailableTools: availableTools, UserRequest: req.Message,
	})
	values["plan_stage_tools"] = strings.Join(agentcontract.NormalizeToolNames(planStageTools), ", ")
	values["execute_stage_tools"] = strings.Join(agentcontract.NormalizeToolNames(executeStageTools), ", ")
	values["execute_tool_descriptions"] = strings.TrimSpace(data.ExecuteToolDescriptions)
	values["ask_user_question_tool_name"] = AskUserQuestionToolName
	values["finalize_planning_tool_name"] = contracts.FinalizePlanningToolName
	values["bash_tool_name"] = "bash"
	values["datetime_tool_name"] = "datetime"
	values["file_read_tool_name"] = "file_read"
	values["file_glob_tool_name"] = "file_glob"
	values["file_grep_tool_name"] = "file_grep"
	values["file_write_tool_name"] = "file_write"
	values["file_edit_tool_name"] = "file_edit"
	values["agent_tool_name"] = contracts.InvokeAgentsToolName
	return values
}
