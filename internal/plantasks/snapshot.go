package plantasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"agent-platform/internal/contracts"
)

const (
	ToolRootDirName = ".tools"
	DirName         = "plan-tasks"
)

type Snapshot struct {
	Version       int            `json:"version"`
	ChatID        string         `json:"chatId"`
	RunID         string         `json:"runId"`
	PlanID        string         `json:"planId"`
	CurrentTaskID string         `json:"currentTaskId"`
	UpdatedAt     int64          `json:"updatedAt"`
	Tasks         []TaskSnapshot `json:"tasks"`
}

type TaskSnapshot struct {
	TaskID      string `json:"taskId"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

func Path(chatsDir string, chatID string, runID string) string {
	fileName := safeRunID(runID) + "_plan.json"
	return filepath.Join(strings.TrimSpace(chatsDir), strings.TrimSpace(chatID), ToolRootDirName, DirName, fileName)
}

func PersistExecutionContext(chatsDir string, execCtx *contracts.ExecutionContext) (string, error) {
	if execCtx == nil {
		return "", nil
	}
	resolvedChatsDir := strings.TrimSpace(chatsDir)
	if resolvedChatsDir == "" {
		resolvedChatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(execCtx.Request.ChatID)
	}
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	return PersistState(resolvedChatsDir, chatID, runID, execCtx.PlanState)
}

func PersistState(chatsDir string, chatID string, runID string, state *contracts.PlanRuntimeState) (string, error) {
	chatsDir = strings.TrimSpace(chatsDir)
	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	if chatsDir == "" || !validChatID(chatID) || runID == "" || state == nil {
		return "", nil
	}

	path := Path(chatsDir, chatID, runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(SnapshotFromState(chatID, runID, state)); err != nil {
		return "", err
	}
	return path, nil
}

func SnapshotFromState(chatID string, runID string, state *contracts.PlanRuntimeState) Snapshot {
	snapshot := Snapshot{
		Version:       1,
		ChatID:        strings.TrimSpace(chatID),
		RunID:         strings.TrimSpace(runID),
		PlanID:        strings.TrimSpace(state.PlanID),
		CurrentTaskID: strings.TrimSpace(state.ActiveTaskID),
		UpdatedAt:     time.Now().UnixMilli(),
		Tasks:         make([]TaskSnapshot, 0, len(state.Tasks)),
	}
	for _, task := range state.Tasks {
		snapshot.Tasks = append(snapshot.Tasks, TaskSnapshot{
			TaskID:      task.TaskID,
			Description: task.Description,
			Status:      task.Status,
		})
	}
	return snapshot
}

func LoadLatest(chatDir string) (*Snapshot, error) {
	chatDir = strings.TrimSpace(chatDir)
	if chatDir == "" {
		return nil, nil
	}
	dir := filepath.Join(chatDir, ToolRootDirName, DirName)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var best *Snapshot
	bestPath := ""
	var bestModTime int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		snapshot, err := LoadFile(path)
		if err != nil {
			continue
		}
		var modTime int64
		if info, infoErr := entry.Info(); infoErr == nil {
			modTime = info.ModTime().UnixMilli()
		}
		if best == nil || newerSnapshot(snapshot, path, modTime, best, bestPath, bestModTime) {
			best = snapshot
			bestPath = path
			bestModTime = modTime
		}
	}
	return best, nil
}

func LoadFile(path string) (*Snapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var snapshot Snapshot
	if err := json.NewDecoder(file).Decode(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func newerSnapshot(current *Snapshot, currentPath string, currentModTime int64, best *Snapshot, bestPath string, bestModTime int64) bool {
	if current.UpdatedAt != best.UpdatedAt {
		return current.UpdatedAt > best.UpdatedAt
	}
	if currentModTime != bestModTime {
		return currentModTime > bestModTime
	}
	return currentPath > bestPath
}

func validChatID(chatID string) bool {
	if strings.TrimSpace(chatID) == "" {
		return false
	}
	if strings.Contains(chatID, "..") || strings.Contains(chatID, "/") || strings.Contains(chatID, `\`) {
		return false
	}
	clean := filepath.Clean(chatID)
	return clean == chatID && clean != "." && clean != string(filepath.Separator)
}

func safeRunID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "run"
	}
	return out
}
