package tools

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

type planTasksSnapshot struct {
	Version       int                `json:"version"`
	ChatID        string             `json:"chatId"`
	RunID         string             `json:"runId"`
	PlanID        string             `json:"planId"`
	CurrentTaskID string             `json:"currentTaskId"`
	UpdatedAt     int64              `json:"updatedAt"`
	Tasks         []planTaskSnapshot `json:"tasks"`
}

type planTaskSnapshot struct {
	TaskID      string `json:"taskId"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

func (t *RuntimeToolExecutor) persistPlanTasksSnapshot(execCtx *ExecutionContext, state *PlanRuntimeState) {
	if execCtx == nil || state == nil {
		return
	}
	chatsDir := ""
	if t != nil {
		chatsDir = strings.TrimSpace(t.cfg.Paths.ChatsDir)
	}
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	if chatsDir == "" {
		log.Printf("[tools][plan] skip plan task snapshot: chats dir unavailable runId=%s", planSnapshotRunID(execCtx))
		return
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(execCtx.Request.ChatID)
	}
	if !chat.ValidChatID(chatID) {
		log.Printf("[tools][plan] skip plan task snapshot: invalid chatId=%q runId=%s", chatID, planSnapshotRunID(execCtx))
		return
	}

	path := planTasksSnapshotPath(chatsDir, chatID, planSnapshotRunID(execCtx))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[tools][plan] write plan task snapshot mkdir failed path=%s err=%v", path, err)
		return
	}
	file, err := os.Create(path)
	if err != nil {
		log.Printf("[tools][plan] write plan task snapshot create failed path=%s err=%v", path, err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(planTasksSnapshotFromState(chatID, planSnapshotRunID(execCtx), state)); err != nil {
		log.Printf("[tools][plan] write plan task snapshot encode failed path=%s err=%v", path, err)
	}
}

func planTasksSnapshotPath(chatsDir string, chatID string, runID string) string {
	fileName := planutil.SafeRunID(runID) + "_plan.json"
	return filepath.Join(strings.TrimSpace(chatsDir), strings.TrimSpace(chatID), chat.ToolRootDirName, chat.ToolPlanTasksDirName, fileName)
}

func planTasksSnapshotFromState(chatID string, runID string, state *PlanRuntimeState) planTasksSnapshot {
	snapshot := planTasksSnapshot{
		Version:       1,
		ChatID:        strings.TrimSpace(chatID),
		RunID:         strings.TrimSpace(runID),
		PlanID:        strings.TrimSpace(state.PlanID),
		CurrentTaskID: strings.TrimSpace(state.ActiveTaskID),
		UpdatedAt:     time.Now().UnixMilli(),
		Tasks:         make([]planTaskSnapshot, 0, len(state.Tasks)),
	}
	for _, task := range state.Tasks {
		snapshot.Tasks = append(snapshot.Tasks, planTaskSnapshot{
			TaskID:      task.TaskID,
			Description: task.Description,
			Status:      task.Status,
		})
	}
	return snapshot
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
