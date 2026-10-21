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

// fileVersionLedgerVersion 2 stores read coverage as line ranges. Ledgers of
// other versions are ignored, so older proofs require a fresh file_read.
const fileVersionLedgerVersion = 2

type fileVersionLedger struct {
	Version int                            `json:"version"`
	Files   map[string]fileVersionSnapshot `json:"files"`
}

type fileVersionSnapshot struct {
	SHA256           string   `json:"sha256"`
	SizeBytes        int64    `json:"sizeBytes"`
	ModifiedUnixMs   int64    `json:"modifiedUnixMs"`
	ObservedAtUnixMs int64    `json:"observedAtUnixMs"`
	Source           string   `json:"source"`
	RunID            string   `json:"runId,omitempty"`
	TotalLines       int      `json:"totalLines"`
	Ranges           [][2]int `json:"ranges,omitempty"`
}

func (item fileVersionSnapshot) readFileSnapshot() ReadFileSnapshot {
	source := strings.TrimSpace(item.Source)
	if source == "" {
		source = "read"
	}
	ranges := make([]LineRange, 0, len(item.Ranges))
	for _, pair := range item.Ranges {
		ranges = append(ranges, LineRange{Start: pair[0], End: pair[1]})
	}
	return ReadFileSnapshot{
		ModifiedUnixMs: item.ModifiedUnixMs,
		SizeBytes:      item.SizeBytes,
		SHA256:         item.SHA256,
		TotalLines:     item.TotalLines,
		Ranges:         NormalizeLineRanges(ranges),
		ReadAtUnixMs:   item.ObservedAtUnixMs,
		Source:         source,
	}
}

func (t *RuntimeToolExecutor) chatReadBeforeWriteEnabled(execCtx *ExecutionContext) bool {
	return t != nil &&
		t.chats != nil &&
		strings.EqualFold(strings.TrimSpace(t.cfg.FileTools.ReadBeforeWriteScope), "chat") &&
		execCtx != nil &&
		!IsReadOnlyToolExecutionPolicy(execCtx.ToolExecutionPolicy) &&
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

func (t *RuntimeToolExecutor) recordChatFileVersion(execCtx *ExecutionContext, path string, snap ReadFileSnapshot) {
	ledgerPath, ok := t.chatFileVersionsPath(execCtx)
	if !ok || strings.TrimSpace(path) == "" || strings.TrimSpace(snap.SHA256) == "" {
		return
	}
	if snap.ReadAtUnixMs <= 0 {
		snap.ReadAtUnixMs = time.Now().UnixMilli()
	}
	t.fileStateMu.Lock()
	defer t.fileStateMu.Unlock()

	ledger := loadFileVersionLedger(ledgerPath)
	// Concurrent tool calls of one Chat may page the same version; merge under
	// the lock so neither page is lost.
	if existing, ok := ledger.Files[path]; ok && strings.TrimSpace(existing.SHA256) != "" {
		prev := existing.readFileSnapshot()
		if !fileSnapshotExpired(prev, time.Now().UnixMilli(), t.fileReadMaxAge()) {
			snap = MergeReadFileSnapshot(prev, snap)
		}
	}
	ranges := make([][2]int, 0, len(snap.Ranges))
	for _, item := range NormalizeLineRanges(snap.Ranges) {
		ranges = append(ranges, [2]int{item.Start, item.End})
	}
	ledger.Files[path] = fileVersionSnapshot{
		SHA256:           snap.SHA256,
		SizeBytes:        snap.SizeBytes,
		ModifiedUnixMs:   snap.ModifiedUnixMs,
		ObservedAtUnixMs: snap.ReadAtUnixMs,
		Source:           strings.TrimSpace(snap.Source),
		RunID:            strings.TrimSpace(execCtx.Session.RunID),
		TotalLines:       snap.TotalLines,
		Ranges:           ranges,
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
	if err != nil {
		return ReadFileSnapshot{}, false
	}
	item, ok := ledger.Files[path]
	if !ok || strings.TrimSpace(item.SHA256) == "" {
		return ReadFileSnapshot{}, false
	}
	return item.readFileSnapshot(), true
}

func loadFileVersionLedger(path string) fileVersionLedger {
	ledger, err := readFileVersionLedger(path)
	if err != nil {
		return emptyFileVersionLedger()
	}
	return ledger
}

func emptyFileVersionLedger() fileVersionLedger {
	return fileVersionLedger{Version: fileVersionLedgerVersion, Files: map[string]fileVersionSnapshot{}}
}

func readFileVersionLedger(path string) (fileVersionLedger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyFileVersionLedger(), nil
	}
	if err != nil {
		return fileVersionLedger{}, err
	}
	var ledger fileVersionLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return fileVersionLedger{}, err
	}
	if ledger.Version != fileVersionLedgerVersion || ledger.Files == nil {
		return emptyFileVersionLedger(), nil
	}
	return ledger, nil
}
