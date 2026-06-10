package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

const (
	fileHistoryDirName      = "file-history"
	fileHistoryManifestName = "manifest.json"
)

type fileHistoryManifest struct {
	Version int                         `json:"version"`
	Files   map[string]fileHistoryEntry `json:"files"`
}

type fileHistoryEntry struct {
	FilePath        string                   `json:"filePath"`
	Original        *fileHistoryVersionEntry `json:"original,omitempty"`
	Current         *fileHistoryVersionEntry `json:"current,omitempty"`
	UpdatedAtUnixMs int64                    `json:"updatedAtUnixMs,omitempty"`
}

type fileHistoryVersionEntry struct {
	Exists          bool   `json:"exists"`
	Blob            string `json:"blob,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	SizeBytes       int    `json:"sizeBytes,omitempty"`
	UpdatedAtUnixMs int64  `json:"updatedAtUnixMs,omitempty"`
}

func (t *RuntimeToolExecutor) recordFileHistory(execCtx *ExecutionContext, filePath string, original []byte, originalExists bool, current []byte, currentExists bool) error {
	if t == nil || t.chats == nil || execCtx == nil {
		return nil
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if !chat.ValidChatID(chatID) || !validFileHistoryRunID(runID) || strings.TrimSpace(filePath) == "" {
		return nil
	}

	runDir := t.fileHistoryRunDir(chatID, runID)
	manifestPath := filepath.Join(runDir, fileHistoryManifestName)
	now := time.Now().UnixMilli()
	key := fileHistoryFileKey(filePath)

	t.fileStateMu.Lock()
	defer t.fileStateMu.Unlock()

	manifest := loadFileHistoryManifest(manifestPath)
	if manifest.Files == nil {
		manifest.Files = map[string]fileHistoryEntry{}
	}
	manifest.Version = 1
	entry := manifest.Files[key]
	entry.FilePath = filePath
	if entry.Original == nil {
		version, err := t.buildFileHistoryVersion(runDir, original, originalExists, now)
		if err != nil {
			return err
		}
		entry.Original = version
	}
	version, err := t.buildFileHistoryVersion(runDir, current, currentExists, now)
	if err != nil {
		return err
	}
	entry.Current = version
	entry.UpdatedAtUnixMs = now
	manifest.Files[key] = entry

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	return atomicWriteFile(manifestPath, data)
}

func (t *RuntimeToolExecutor) ReadFileHistory(chatID string, runID string, filePath string, version string) (string, error) {
	if t == nil || t.chats == nil {
		return "", ErrNotImplemented
	}
	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	filePath = strings.TrimSpace(filePath)
	version = strings.TrimSpace(version)
	if !chat.ValidChatID(chatID) || !validFileHistoryRunID(runID) || !validFileHistoryFilePath(filePath) {
		return "", os.ErrPermission
	}

	manifestPath := filepath.Join(t.fileHistoryRunDir(chatID, runID), fileHistoryManifestName)
	t.fileStateMu.Lock()
	defer t.fileStateMu.Unlock()

	manifest, err := readFileHistoryManifest(manifestPath)
	if err != nil {
		return "", err
	}
	entry, ok := manifest.Files[fileHistoryFileKey(filePath)]
	if !ok || entry.FilePath != filePath {
		return "", os.ErrNotExist
	}

	var selected *fileHistoryVersionEntry
	switch version {
	case "original":
		selected = entry.Original
	case "current":
		selected = entry.Current
	default:
		return "", os.ErrPermission
	}
	if selected == nil {
		return "", os.ErrNotExist
	}
	if !selected.Exists {
		return "", nil
	}
	if strings.TrimSpace(selected.Blob) == "" {
		return "", os.ErrNotExist
	}
	blobPath := filepath.Join(t.fileHistoryRunDir(chatID, runID), "blobs", selected.Blob)
	if !fileHistoryBlobNameValid(selected.Blob) {
		return "", os.ErrPermission
	}
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *RuntimeToolExecutor) buildFileHistoryVersion(runDir string, content []byte, exists bool, updatedAt int64) (*fileHistoryVersionEntry, error) {
	entry := &fileHistoryVersionEntry{
		Exists:          exists,
		UpdatedAtUnixMs: updatedAt,
	}
	if !exists {
		return entry, nil
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	blobName := sha + ".txt"
	blobDir := filepath.Join(runDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, err
	}
	blobPath := filepath.Join(blobDir, blobName)
	if _, err := os.Stat(blobPath); errors.Is(err, os.ErrNotExist) {
		if err := atomicWriteFile(blobPath, content); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	entry.Blob = blobName
	entry.SHA256 = sha
	entry.SizeBytes = len(content)
	return entry, nil
}

func (t *RuntimeToolExecutor) fileHistoryRunDir(chatID string, runID string) string {
	return filepath.Join(t.chats.ChatDir(chatID), chat.ToolRootDirName, chat.ToolStateDirName, fileHistoryDirName, runID)
}

func loadFileHistoryManifest(path string) fileHistoryManifest {
	manifest, err := readFileHistoryManifest(path)
	if err != nil {
		return fileHistoryManifest{Version: 1, Files: map[string]fileHistoryEntry{}}
	}
	if manifest.Version <= 0 {
		manifest.Version = 1
	}
	if manifest.Files == nil {
		manifest.Files = map[string]fileHistoryEntry{}
	}
	return manifest
}

func readFileHistoryManifest(path string) (fileHistoryManifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileHistoryManifest{Version: 1, Files: map[string]fileHistoryEntry{}}, err
	}
	if err != nil {
		return fileHistoryManifest{}, err
	}
	var manifest fileHistoryManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fileHistoryManifest{}, err
	}
	if manifest.Files == nil {
		manifest.Files = map[string]fileHistoryEntry{}
	}
	return manifest, nil
}

func fileHistoryFileKey(filePath string) string {
	sum := sha256.Sum256([]byte(filePath))
	return hex.EncodeToString(sum[:])
}

func validFileHistoryRunID(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" || strings.Contains(runID, "..") || strings.Contains(runID, "/") || strings.Contains(runID, `\`) {
		return false
	}
	clean := filepath.Clean(runID)
	return clean == runID && clean != "." && clean != string(filepath.Separator)
}

func validFileHistoryFilePath(filePath string) bool {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || strings.ContainsRune(filePath, 0) {
		return false
	}
	slashed := filepath.ToSlash(filePath)
	return slashed != ".." && !strings.HasPrefix(slashed, "../") && !strings.Contains(slashed, "/../")
}

func fileHistoryBlobNameValid(name string) bool {
	if len(name) != len(hex.EncodeToString(make([]byte, sha256.Size)))+len(".txt") || !strings.HasSuffix(name, ".txt") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".txt"))
	return err == nil
}
