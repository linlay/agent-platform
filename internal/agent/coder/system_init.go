package coder

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type SystemInitSpec struct {
	CacheStage            string
	FingerprintStage      string
	PromptStage           string
	Mode                  string
	Stage                 string
	ToolNames             []string
	SystemPrompt          string
	UseSharedSystemPrompt bool
	IncludeAfterCallHints bool
}

func PlanningSystemInitSpecs(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) []SystemInitSpec {
	return []SystemInitSpec{
		PlanningPlanSystemInitSpec(),
		PlanningExecuteSystemInitSpec(session, req, settings),
	}
}

func PlanningPlanSystemInitSpec() SystemInitSpec {
	return SystemInitSpec{
		CacheStage:            "coder-plan",
		FingerprintStage:      "coder-plan",
		PromptStage:           "coder-plan",
		Mode:                  "coder",
		Stage:                 "plan",
		ToolNames:             PlanningModePlanTools(),
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
	}
}

func PlanningExecuteSystemInitSpec(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings) SystemInitSpec {
	executeTools := PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
	return SystemInitSpec{
		CacheStage:       "coder-execute",
		FingerprintStage: "coder-execute",
		PromptStage:      "coder-execute",
		Mode:             "coder",
		Stage:            "execute",
		ToolNames:        executeTools,
		SystemPrompt:     PlanningExecutionSystemPrompt(session, req, settings, PlanningModePlanTools(), executeTools, DefaultExecuteSystemPrompt),
	}
}
