package llm

import (
	"context"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/agent/planmode"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type planningRuntimeAdapter struct {
	engine *LLMAgentEngine
	mode   string
}

// Settings resolves the planning prompt for the Agent's mode: a mode-specific
// prompt when configured, otherwise the shared one. An empty result lets
// planmode fall back to its built-in neutral prompt.
func (a planningRuntimeAdapter) Settings() planmode.RuntimeSettings {
	e := a.engine
	if e == nil {
		return planmode.RuntimeSettings{}
	}
	prompt := e.cfg.Prompts.PlanningMode.PlanningPrompt
	if agentcoder.IsMode(a.mode) && strings.TrimSpace(e.cfg.CoderPrompts.PlanningPrompt) != "" {
		prompt = e.cfg.CoderPrompts.PlanningPrompt
	}
	return planmode.RuntimeSettings{
		PlanningPrompt:          prompt,
		DefaultPlanningMaxSteps: e.cfg.Defaults.CoderPlanning.MaxSteps,
	}
}

func (a planningRuntimeAdapter) NewStageRunStream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession, allowToolUse bool, options planmode.StageRunOptions) (contracts.AgentStream, error) {
	e := a.engine
	return e.newRunStreamWithOptions(ctx, req, session, allowToolUse, runStreamOptions{
		ExecCtx:                      options.ExecCtx,
		Messages:                     options.Messages,
		ToolNames:                    options.ToolNames,
		ModelKey:                     options.ModelKey,
		MaxSteps:                     options.MaxSteps,
		Stage:                        options.Stage,
		ToolChoice:                   options.ToolChoice,
		PreserveProvidedSystemPrompt: options.PreserveProvidedSystemPrompt,
		PostToolHook:                 options.PostToolHook,
		PreserveSteersOnFinish:       options.PreserveSteersOnFinish,
	})
}

func (a planningRuntimeAdapter) BuildCurrentMessagesForRequest(req api.QueryRequest, session contracts.QuerySession, fallbackVision bool) []map[string]any {
	e := a.engine
	return e.buildCurrentMessagesForRequest(req, session, fallbackVision)
}

func (a planningRuntimeAdapter) ToolDefinitions() []api.ToolDetailResponse {
	e := a.engine
	if e == nil || e.tools == nil {
		return nil
	}
	return e.tools.Definitions()
}
