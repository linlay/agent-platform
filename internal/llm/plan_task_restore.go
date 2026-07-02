package llm

import (
	"log"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/plantasks"
)

func (e *LLMAgentEngine) restorePlanTasksForRun(execCtx *ExecutionContext, session *QuerySession, stage string, toolDefs []api.ToolDetailResponse) {
	if e == nil || execCtx == nil || session == nil || execCtx.PlanState != nil {
		return
	}
	toolNames := toolNamesFromDefinitions(toolDefs, session.ToolNames)
	if !shouldUsePlanTaskContextForStage(stage, toolNames, session.PlanningMode) {
		return
	}
	chatsDir := strings.TrimSpace(e.cfg.Paths.ChatsDir)
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	if _, err := plantasks.RestoreExecutionContext(chatsDir, execCtx); err != nil {
		log.Printf("[llm][plan] restore plan task snapshot failed runId=%s err=%v", session.RunID, err)
		return
	}
	if context := plantasks.FormatStateContext(execCtx.PlanState); context != "" {
		session.PlanTaskContext = context
		execCtx.Session.PlanTaskContext = context
	}
}

func shouldUsePlanTaskContextForStage(stage string, toolNames []string, planningMode bool) bool {
	if planningMode {
		return false
	}
	normalizedStage := strings.ToLower(strings.TrimSpace(stage))
	if normalizedStage == "plan" || strings.HasSuffix(normalizedStage, "-plan") || strings.Contains(normalizedStage, "planning") {
		return false
	}
	return hasPlanTaskRestoreTool(toolNames)
}

func hasPlanTaskRestoreTool(toolNames []string) bool {
	for _, name := range toolNames {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case PlanGetTasksToolName, PlanUpdateTaskToolName:
			return true
		}
	}
	return false
}

func cachedSystemInitHasPlanTaskContext(system openAIMessage, context string) bool {
	context = strings.TrimSpace(context)
	if context == "" {
		return true
	}
	content, ok := system.Content.(string)
	if !ok {
		return false
	}
	return strings.Contains(content, context)
}
