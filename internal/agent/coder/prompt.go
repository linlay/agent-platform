package coder

import (
	"strings"

	"agent-platform/internal/agent/planmode"
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
	return planmode.RenderPromptTemplate(session.ModeSystemPrompt, planmode.PromptTemplateValues(session, req, planmode.PromptTemplateData{
		AvailableTools: toolNames,
	}))
}
