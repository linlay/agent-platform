package coder

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func SystemInitCacheKey(stage string) string {
	stage = strings.ToLower(strings.TrimSpace(stage))
	switch {
	case strings.HasPrefix(stage, PlanStage):
		return PlanCacheKey
	case strings.HasPrefix(stage, ExecuteStage):
		return ExecuteCacheKey
	default:
		return MainCacheKey
	}
}

func PlanningSystemInitSpecs(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) []agentcontract.SystemInitSpec {
	return []agentcontract.SystemInitSpec{
		PlanningPlanSystemInitSpec(),
		PlanningExecuteSystemInitSpec(session, req, settings),
	}
}

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              MainCacheKey,
		FingerprintStage:      "main",
		PromptStage:           MainStage,
		Mode:                  MainStage,
		Stage:                 "main",
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}

func PlanningPlanSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              PlanCacheKey,
		FingerprintStage:      PlanStage,
		PromptStage:           PlanStage,
		Mode:                  MainStage,
		Stage:                 "plan",
		ToolNames:             PlanningModePlanTools(),
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}

func PlanningExecuteSystemInitSpec(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) agentcontract.SystemInitSpec {
	executeTools := PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
	return agentcontract.SystemInitSpec{
		CacheKey:         ExecuteCacheKey,
		FingerprintStage: ExecuteStage,
		PromptStage:      ExecuteStage,
		Mode:             MainStage,
		Stage:            "execute",
		ToolNames:        executeTools,
		SystemPrompt:     PlanningExecutionSystemPrompt(session, req, settings, PlanningModePlanTools(), executeTools, DefaultExecuteSystemPrompt),
	}
}
