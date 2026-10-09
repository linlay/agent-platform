package kbase

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !IsMode(session.Mode) {
		return ""
	}
	editing := strings.EqualFold(strings.TrimSpace(stage), EditingStage)
	if !editing && !strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	// Every part comes from agent-prompt.yml; an empty part is skipped.
	parts := []string{session.KBaseModePrompts.Capability, session.ModeSystemPrompt, session.KBaseModePrompts.Workspace}
	if editing {
		parts = append(parts, session.KBaseModePrompts.Editing)
	}
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			sections = append(sections, part)
		}
	}
	prompt := strings.Join(sections, "\n\n")
	if len(toolNames) == 0 {
		toolNames = session.ToolNames
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
		AgentKey:           session.AgentKey,
		AgentName:          session.AgentName,
		Mode:               session.Mode,
		PlanningMode:       session.PlanningMode,
		EditingMode:        session.EditingMode,
		WorkspaceDir:       workspaceDir,
		ChatDir:            chatDir,
		AvailableTools:     toolNames,
		UserRequest:        req.Message,
	})
	return agentcontract.RenderPromptTemplate(prompt, values)
}
