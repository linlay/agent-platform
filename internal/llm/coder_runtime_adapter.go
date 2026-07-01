package llm

import (
	"context"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func (e *LLMAgentEngine) Settings() agentcoder.RuntimeSettings {
	if e == nil {
		return agentcoder.RuntimeSettings{}
	}
	return agentcoder.RuntimeSettings{
		PlanningPrompt:                  e.cfg.CoderPrompts.PlanningPrompt,
		DefaultPlanMaxSteps:             e.cfg.Defaults.Plan.MaxSteps,
		DefaultPlanMaxWorkRoundsPerTask: e.cfg.Defaults.Plan.MaxWorkRoundsPerTask,
	}
}

func (e *LLMAgentEngine) NewStageRunStream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession, allowToolUse bool, options agentcoder.StageRunOptions) (contracts.AgentStream, error) {
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
	})
}

func (e *LLMAgentEngine) BuildCurrentMessagesForRequest(req api.QueryRequest, session contracts.QuerySession, fallbackVision bool) []map[string]any {
	return e.buildCurrentMessagesForRequest(req, session, fallbackVision)
}

func (e *LLMAgentEngine) ToolDefinitions() []api.ToolDetailResponse {
	if e == nil || e.tools == nil {
		return nil
	}
	return e.tools.Definitions()
}

func (e *LLMAgentEngine) BuildExecuteSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) []contracts.SystemInitProfile {
	if e == nil {
		return nil
	}
	session.ResolvedStageSettings = settings
	profile := buildCoderPlanningExecuteSystemInitProfile(session, req, settings, e.ToolDefinitions())
	(SystemInitProfileBuilder{Models: e.models}).applyRequestProfile(&profile, session, req)
	return []contracts.SystemInitProfile{profile}
}
