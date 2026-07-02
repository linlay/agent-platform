package tools

import (
	"log"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/plantasks"
)

type planTasksSnapshot = plantasks.Snapshot
type planTaskSnapshot = plantasks.TaskSnapshot

func (t *RuntimeToolExecutor) persistPlanTasksSnapshot(execCtx *ExecutionContext, state *PlanRuntimeState) {
	if execCtx == nil || state == nil {
		return
	}
	if execCtx.PlanState == nil {
		execCtx.PlanState = state
	}
	chatsDir := ""
	if t != nil {
		chatsDir = strings.TrimSpace(t.cfg.Paths.ChatsDir)
	}
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	path, err := plantasks.PersistExecutionContext(chatsDir, execCtx)
	if err != nil {
		log.Printf("[tools][plan] write plan task snapshot failed runId=%s path=%s err=%v", planSnapshotRunID(execCtx), path, err)
		return
	}
}

func planSnapshotRunID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	return runID
}
