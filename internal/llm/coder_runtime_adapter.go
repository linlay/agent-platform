package llm

import (
	"context"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type coderRuntimeAdapter struct {
	engine *LLMAgentEngine
}

func (a coderRuntimeAdapter) Settings() agentcoder.RuntimeSettings {
	e := a.engine
	if e == nil {
		return agentcoder.RuntimeSettings{}
	}
	return agentcoder.RuntimeSettings{
		PlanningPrompt:                  e.cfg.CoderPrompts.PlanningPrompt,
		DefaultPlanMaxSteps:             e.cfg.Defaults.Plan.MaxSteps,
		DefaultPlanMaxWorkRoundsPerTask: e.cfg.Defaults.Plan.MaxWorkRoundsPerTask,
	}
}

func (a coderRuntimeAdapter) NewStageRunStream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession, allowToolUse bool, options agentcoder.StageRunOptions) (contracts.AgentStream, error) {
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
	})
}

func (a coderRuntimeAdapter) BuildCurrentMessagesForRequest(req api.QueryRequest, session contracts.QuerySession, fallbackVision bool) []map[string]any {
	e := a.engine
	return e.buildCurrentMessagesForRequest(req, session, fallbackVision)
}

func (a coderRuntimeAdapter) ToolDefinitions() []api.ToolDetailResponse {
	e := a.engine
	if e == nil || e.tools == nil {
		return nil
	}
	return e.tools.Definitions()
}

func (a coderRuntimeAdapter) BuildExecuteSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) []contracts.SystemInitProfile {
	e := a.engine
	if e == nil {
		return nil
	}
	session.ResolvedStageSettings = settings
	profile := buildCoderPlanningExecuteSystemInitProfile(session, req, settings, a.ToolDefinitions())
	(SystemInitProfileBuilder{Models: e.models}).applyRequestProfile(&profile, session, req)
	return []contracts.SystemInitProfile{profile}
}
