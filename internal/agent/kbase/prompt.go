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
	if !strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	// Every part comes from agent-prompt.yml; an empty part is skipped. The
	// prompt never depends on editingMode, which only changes what may be written.
	parts := []string{session.ModeSystemPrompt, session.KBaseModePrompts.Workspace}
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
		WorkspaceDir:       workspaceDir,
		ChatDir:            chatDir,
		AvailableTools:     toolNames,
		UserRequest:        req.Message,
	})
	return agentcontract.RenderPromptTemplate(prompt, values)
}
