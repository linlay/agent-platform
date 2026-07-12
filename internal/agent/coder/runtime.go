package coder

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type RuntimeSettings struct {
	PlanningPrompt          string
	DefaultPlanningMaxSteps int
}

type StageRunOptions struct {
	ExecCtx                      *contracts.ExecutionContext
	Messages                     []contracts.ModelMessage
	ToolNames                    []string
	ModelKey                     string
	MaxSteps                     int
	Stage                        string
	ToolChoice                   string
	PreserveProvidedSystemPrompt bool
	PostToolHook                 func(toolName string, toolID string) contracts.PostToolHookResult
}

type Runtime interface {
	Settings() RuntimeSettings
	NewStageRunStream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession, allowToolUse bool, options StageRunOptions) (contracts.AgentStream, error)
	BuildCurrentMessagesForRequest(req api.QueryRequest, session contracts.QuerySession, fallbackVision bool) []map[string]any
	ToolDefinitions() []api.ToolDetailResponse
	BuildExecuteSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, settings contracts.CoderPlanningSettings) []contracts.SystemInitProfile
}

type AccumulatedMessageStream interface {
	AccumulatedMessages() []contracts.ModelMessage
}
