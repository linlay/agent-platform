package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

type fileVersionLedger struct {
	Version int                            `json:"version"`
	Files   map[string]fileVersionSnapshot `json:"files"`
}

type fileVersionSnapshot struct {
	SHA256           string `json:"sha256"`
	SizeBytes        int64  `json:"sizeBytes"`
	ModifiedUnixMs   int64  `json:"modifiedUnixMs"`
	ObservedAtUnixMs int64  `json:"observedAtUnixMs"`
	Source           string `json:"source"`
	RunID            string `json:"runId,omitempty"`
	Offset           int64  `json:"offset,omitempty"`
	Limit            int64  `json:"limit,omitempty"`
	Truncated        bool   `json:"truncated,omitempty"`
}

func (t *RuntimeToolExecutor) chatReadBeforeWriteEnabled(execCtx *ExecutionContext) bool {
	return t != nil &&
		t.chats != nil &&
		strings.EqualFold(strings.TrimSpace(t.cfg.FileTools.ReadBeforeWriteScope), "chat") &&
		execCtx != nil &&
		strings.TrimSpace(execCtx.Session.ChatID) != ""
}

func (t *RuntimeToolExecutor) chatFileVersionsPath(execCtx *ExecutionContext) (string, bool) {
	if !t.chatReadBeforeWriteEnabled(execCtx) {
		return "", false
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if !chat.ValidChatID(chatID) {
		return "", false
	}
	return filepath.Join(t.chats.ChatDir(chatID), chat.ToolRootDirName, chat.ToolStateDirName, chat.FileVersionsFileName), true
}

func (t *RuntimeToolExecutor) recordChatFileVersion(execCtx *ExecutionContext, path string, snap ReadFileSnapshot, source string, truncated bool) {
	ledgerPath, ok := t.chatFileVersionsPath(execCtx)
	if !ok || strings.TrimSpace(path) == "" || strings.TrimSpace(snap.SHA256) == "" {
		return
	}
	observedAt := snap.ReadAtUnixMs
	if observedAt <= 0 {
		observedAt = time.Now().UnixMilli()
	}
	t.fileStateMu.Lock()
	defer t.fileStateMu.Unlock()

	ledger := loadFileVersionLedger(ledgerPath)
	if ledger.Files == nil {
		ledger.Files = map[string]fileVersionSnapshot{}
	}
	ledger.Version = 1
	ledger.Files[path] = fileVersionSnapshot{
		SHA256:           snap.SHA256,
		SizeBytes:        snap.SizeBytes,
		ModifiedUnixMs:   snap.ModifiedUnixMs,
		ObservedAtUnixMs: observedAt,
		Source:           strings.TrimSpace(source),
		RunID:            strings.TrimSpace(execCtx.Session.RunID),
		Offset:           snap.Offset,
		Limit:            snap.Limit,
		Truncated:        truncated,
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		return
	}
	_ = atomicWriteFile(ledgerPath, data)
}

func (t *RuntimeToolExecutor) loadChatFileVersionSnapshot(execCtx *ExecutionContext, path string) (ReadFileSnapshot, bool) {
	ledgerPath, ok := t.chatFileVersionsPath(execCtx)
	if !ok || strings.TrimSpace(path) == "" {
		return ReadFileSnapshot{}, false
	}
	t.fileStateMu.Lock()
	defer t.fileStateMu.Unlock()

	ledger, err := readFileVersionLedger(ledgerPath)
	if err != nil || ledger.Files == nil {
		return ReadFileSnapshot{}, false
	}
	item, ok := ledger.Files[path]
	if !ok || strings.TrimSpace(item.SHA256) == "" {
		return ReadFileSnapshot{}, false
	}
	return ReadFileSnapshot{
		ModifiedUnixMs: item.ModifiedUnixMs,
		SizeBytes:      item.SizeBytes,
		SHA256:         item.SHA256,
		Offset:         item.Offset,
		Limit:          item.Limit,
		ReadAtUnixMs:   item.ObservedAtUnixMs,
	}, true
}

func loadFileVersionLedger(path string) fileVersionLedger {
	ledger, err := readFileVersionLedger(path)
	if err != nil {
		return fileVersionLedger{Version: 1, Files: map[string]fileVersionSnapshot{}}
	}
	if ledger.Version <= 0 {
		ledger.Version = 1
	}
	if ledger.Files == nil {
		ledger.Files = map[string]fileVersionSnapshot{}
	}
	return ledger
}

func readFileVersionLedger(path string) (fileVersionLedger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileVersionLedger{Version: 1, Files: map[string]fileVersionSnapshot{}}, nil
	}
	if err != nil {
		return fileVersionLedger{}, err
	}
	var ledger fileVersionLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return fileVersionLedger{}, err
	}
	if ledger.Files == nil {
		ledger.Files = map[string]fileVersionSnapshot{}
	}
	return ledger, nil
}
