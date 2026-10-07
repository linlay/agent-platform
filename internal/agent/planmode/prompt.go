package planmode

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// DefaultPlanningPrompt is used when no planning prompt is configured for the
// Agent's mode. It is mode neutral; mode-specific prompts replace it.
const DefaultPlanningPrompt = `You are in planning mode.

Planning rules:
1. Planning mode does not carry out the request. You may use only these tools: {{planning_stage_tools}}.
2. Investigate with the available tools before asking. Do not ask questions that the tools can answer.
3. Do not change files, data, or external systems, even if an available tool could.
4. When intent, scope, acceptance criteria, or tradeoffs are unclear, ask the user with {{ask_user_question_tool_name}} if it is available.
5. Do not output the plan as normal assistant text. When the plan is decision-complete, call {{finalize_planning_tool_name}} exactly once with the complete Markdown plan, including one top-level heading.
6. The plan must state the goal, the concrete steps in order, how the result will be verified, and the assumptions made.
7. The user must confirm the plan before it is executed. After confirmation the plan is executed without further questions, so resolve open decisions now.

Tools available after confirmation: {{execute_stage_tools}}

{{execute_tool_descriptions}}`

type PromptTemplateData struct {
	AvailableTools          []string
	PlanningStageTools      []string
	ExecuteStageTools       []string
	ExecuteToolDescriptions string
}

func RenderPromptTemplate(prompt string, values map[string]string) string {
	return agentcontract.RenderPromptTemplate(prompt, values)
}

// PromptTemplateValues are the placeholders shared by planning prompts and by
// mode system prompts that describe the planning and execution tool sets.
func PromptTemplateValues(session contracts.QuerySession, req api.QueryRequest, data PromptTemplateData) map[string]string {
	availableTools := data.AvailableTools
	if len(availableTools) == 0 {
		availableTools = session.ToolNames
	}
	planningStageTools := data.PlanningStageTools
	if len(planningStageTools) == 0 {
		planningStageTools = PlanningTools(session, nil)
	}
	executeStageTools := data.ExecuteStageTools
	if len(executeStageTools) == 0 {
		executeStageTools = ConfirmedPlanTools(session, nil)
	}
	workspaceDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.WorkspaceDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
		session.WorkspaceRoot,
	)
	chatDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.ChatDir,
		session.RuntimeContext.SandboxPaths.ChatDir,
	)
	if session.AgentHasRuntimeSandbox {
		workspaceDir = agentcontract.FirstNonBlank(session.RuntimeContext.SandboxPaths.WorkspaceDir, workspaceDir)
		chatDir = agentcontract.FirstNonBlank(session.RuntimeContext.SandboxPaths.ChatDir, chatDir)
	}
	values := agentcontract.CommonPromptValues(agentcontract.PromptContext{
		LanguagePreference: session.Locale,
		AgentKey:           session.AgentKey, AgentName: session.AgentName, Mode: session.Mode,
		PlanningMode: session.PlanningMode, WorkspaceDir: workspaceDir, ChatDir: chatDir,
		AvailableTools: availableTools, UserRequest: req.Message,
	})
	values["planning_stage_tools"] = strings.Join(agentcontract.NormalizeToolNames(planningStageTools), ", ")
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
