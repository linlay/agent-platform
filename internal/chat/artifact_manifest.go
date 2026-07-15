package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ArtifactManifestFileName = "artifacts.json"

// ArtifactManifest is the chat-scoped source of truth for published
// artifacts. It is intentionally stored below .tools so it cannot be served
// as a user resource.
type ArtifactManifest struct {
	ChatID string                 `json:"chatId"`
	Items  []ArtifactManifestItem `json:"items"`
}

type ArtifactManifestItem struct {
	ArtifactItemState
	RunID       string `json:"runId"`
	PublishedAt int64  `json:"publishedAt"`
}

// ArtifactManifestWriter is deliberately narrow so runtime tool test doubles
// do not need the full chat.Store interface.
type ArtifactManifestWriter interface {
	AppendArtifactManifest(chatID string, runID string, publishedAt int64, artifacts []map[string]any) error
}

func artifactManifestPath(chatDir string) string {
	return filepath.Join(chatDir, ToolRootDirName, ArtifactManifestFileName)
}

func (s *FileStore) AppendArtifactManifest(chatID string, runID string, publishedAt int64, artifacts []map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidChatID(chatID) || strings.TrimSpace(runID) == "" || publishedAt <= 0 {
		return os.ErrPermission
	}
	if summary, err := s.loadSummary(chatID); err != nil {
		return err
	} else if summary == nil {
		return ErrChatNotFound
	}
	return appendArtifactManifest(s.ChatDir(chatID), chatID, runID, publishedAt, artifacts)
}

func appendArtifactManifest(chatDir string, chatID string, runID string, publishedAt int64, artifacts []map[string]any) error {
	items := artifactItemsFromValue(artifacts)
	if len(items) == 0 {
		return errors.New("artifact manifest requires at least one published artifact")
	}
	manifest, found, err := loadArtifactManifest(chatDir, chatID)
	if err != nil {
		return err
	}
	if !found {
		manifest = ArtifactManifest{ChatID: chatID, Items: make([]ArtifactManifestItem, 0, len(items))}
	}

	for _, artifact := range items {
		entry := ArtifactManifestItem{
			ArtifactItemState: artifact,
			RunID:             runID,
			PublishedAt:       publishedAt,
		}
		manifest.Items = append(manifest.Items, entry)
	}
	return writeArtifactManifest(artifactManifestPath(chatDir), manifest)
}

func loadArtifactManifest(chatDir string, chatID string) (ArtifactManifest, bool, error) {
	path := artifactManifestPath(chatDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ArtifactManifest{}, false, nil
	}
	if err != nil {
		return ArtifactManifest{}, false, err
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ArtifactManifest{}, false, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if strings.TrimSpace(manifest.ChatID) != strings.TrimSpace(chatID) {
		return ArtifactManifest{}, false, fmt.Errorf("artifact manifest chatId mismatch")
	}
	return manifest, true, nil
}

func loadArtifactStateFromManifest(chatDir string, chatID string) (*ArtifactState, error) {
	manifest, found, err := loadArtifactManifest(chatDir, chatID)
	if err != nil || !found || len(manifest.Items) == 0 {
		return nil, err
	}
	state := &ArtifactState{Items: make([]ArtifactItemState, 0, len(manifest.Items))}
	for _, item := range manifest.Items {
		state.Items = append(state.Items, item.ArtifactItemState)
	}
	return state, nil
}

func writeArtifactManifest(path string, manifest ArtifactManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifacts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
