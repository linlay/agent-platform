package kbase

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	corekbase "agent-platform/internal/kbase"
)

const DefaultModePrompt = "KBASE Mode\nYou are a dedicated knowledge-base question-answering agent."

const DefaultSystemPrompt = corekbase.DefaultCapabilityPrompt + "\n\n" + DefaultModePrompt

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), Mode) ||
		!strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	prompt := strings.TrimSpace(session.ModeSystemPrompt)
	if prompt == "" {
		prompt = DefaultSystemPrompt
	} else if !strings.Contains(prompt, "Knowledge Base Capability") {
		prompt = corekbase.DefaultCapabilityPrompt + "\n\n" + prompt
	}
	if len(toolNames) == 0 {
		toolNames = session.ToolNames
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
		AgentKey:       session.AgentKey,
		AgentName:      session.AgentName,
		Mode:           session.Mode,
		PlanningMode:   session.PlanningMode,
		WorkspaceDir:   workspaceDir,
		ChatDir:        chatDir,
		AvailableTools: toolNames,
		UserRequest:    req.Message,
	})
	return agentcontract.RenderPromptTemplate(prompt, values)
}
