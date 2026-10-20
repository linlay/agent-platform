package chatresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"agent-platform/internal/chat"
)

var ErrArtifactNotFound = errors.New("artifact_not_found")
var ErrArtifactAmbiguous = errors.New("artifact_ambiguous")
var ErrArtifactChanged = errors.New("artifact_changed")

type Artifact struct {
	ChatID      string `json:"chatId"`
	RunID       string `json:"runId"`
	ArtifactID  string `json:"artifactId"`
	PublishedAt int64  `json:"publishedAt"`
	Name        string `json:"name"`
	MIMEType    string `json:"mimeType"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
}

func projectArtifact(chatID string, item chat.ArtifactManifestItem) Artifact {
	return Artifact{chatID, item.RunID, item.ArtifactID, item.PublishedAt, item.Name, item.MimeType, item.SizeBytes, item.SHA256}
}
func (s *Service) artifactManifest(chatID string) ([]chat.ArtifactManifestItem, error) {
	if !chat.ValidChatID(chatID) {
		return nil, ErrArtifactNotFound
	}
	reader, ok := s.chats.(interface {
		PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
	})
	if !ok {
		return nil, ErrNotConfigured
	}
	return reader.PublishedArtifacts(chatID)
}
func (s *Service) ListArtifacts(chatID, runID string, offset, limit int) ([]Artifact, bool, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, false, ErrArtifactNotFound
	}
	entries, err := s.artifactManifest(chatID)
	if err != nil {
		return nil, false, err
	}
	items := []Artifact{}
	matched := 0
	for _, entry := range entries {
		if runID != "" && entry.RunID != runID {
			continue
		}
		matched++
		if matched <= offset {
			continue
		}
		if len(items) == limit {
			return items, true, nil
		}
		items = append(items, projectArtifact(chatID, entry))
	}
	return items, false, nil
}
func (s *Service) findArtifact(chatID, id, runID string) (chat.ArtifactManifestItem, error) {
	entries, err := s.artifactManifest(chatID)
	if err != nil {
		return chat.ArtifactManifestItem{}, err
	}
	var found chat.ArtifactManifestItem
	count := 0
	for _, entry := range entries {
		if id != "" && entry.ArtifactID == id && (runID == "" || entry.RunID == runID) {
			found = entry
			count++
		}
	}
	if count == 0 {
		return found, ErrArtifactNotFound
	}
	if count != 1 {
		return found, ErrArtifactAmbiguous
	}
	return found, nil
}

func (s *Service) findArtifactByRef(chatID, sourceRef string) (chat.ArtifactManifestItem, error) {
	canonicalRef, err := canonicalArtifactRef(chatID, sourceRef)
	if err != nil {
		return chat.ArtifactManifestItem{}, ErrArtifactNotFound
	}
	entries, err := s.artifactManifest(chatID)
	if err != nil {
		return chat.ArtifactManifestItem{}, err
	}
	var found chat.ArtifactManifestItem
	matched := false
	for _, entry := range entries {
		if chat.BareChatScopeRef(entry.URL) == canonicalRef {
			found = entry
			matched = true
		}
	}
	if !matched {
		return chat.ArtifactManifestItem{}, ErrArtifactNotFound
	}
	return found, nil
}

func canonicalArtifactRef(chatID, sourceRef string) (string, error) {
	// An @chat/ path is literal, so "?" and "#" are file name characters
	// there; only the encoded bare form reserves them.
	sourceRef = chat.BareChatScopeRef(sourceRef)
	if !chat.ValidChatID(chatID) || sourceRef == "" || sourceRef != strings.TrimSpace(sourceRef) ||
		strings.ContainsAny(sourceRef, `\?#`) {
		return "", ErrArtifactNotFound
	}
	owner, relative, err := chat.ParseResourceKey(chatID + "/" + sourceRef)
	if err != nil || owner != chatID {
		return "", ErrArtifactNotFound
	}
	segments := strings.Split(relative, "/")
	if len(segments) != 3 || segments[0] != "artifacts" || segments[1] == "" || segments[2] == "" {
		return "", ErrArtifactNotFound
	}
	canonical, err := chat.BuildChatScopeRef(relative)
	if err != nil || canonical != sourceRef {
		return "", ErrArtifactNotFound
	}
	return canonical, nil
}
func (s *Service) GetArtifact(chatID, id, runID string) (Artifact, error) {
	entry, err := s.findArtifact(chatID, id, runID)
	return projectArtifact(chatID, entry), err
}
func (s *Service) OpenArtifact(ctx context.Context, chatID, id, runID string) (*os.File, Artifact, error) {
	entry, err := s.findArtifact(chatID, id, runID)
	if err != nil {
		return nil, Artifact{}, err
	}
	return s.openArtifact(ctx, chatID, entry)
}

func (s *Service) OpenArtifactByRef(ctx context.Context, chatID, sourceRef string) (*os.File, Artifact, error) {
	entry, err := s.findArtifactByRef(chatID, sourceRef)
	if err != nil {
		return nil, Artifact{}, err
	}
	return s.openArtifact(ctx, chatID, entry)
}

func (s *Service) openArtifact(ctx context.Context, chatID string, entry chat.ArtifactManifestItem) (*os.File, Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, Artifact{}, err
	}
	// Only published ChatScope references, never a caller-supplied file or URL.
	ref := chat.BareChatScopeRef(entry.URL)
	if !strings.HasPrefix(ref, "artifacts/") {
		return nil, Artifact{}, ErrArtifactNotFound
	}
	key := chatID + "/" + ref
	owner, relative, err := chat.ParseResourceKey(key)
	if err != nil || owner != chatID || !strings.HasPrefix(relative, "artifacts/") {
		return nil, Artifact{}, ErrArtifactNotFound
	}
	path, err := s.ResolveResource(key)
	if err != nil {
		return nil, Artifact{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Artifact{}, err
	}
	valid := false
	defer func() {
		if !valid {
			f.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer stop()
	info, err := f.Stat()
	if err := ctx.Err(); err != nil {
		return nil, Artifact{}, err
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.SizeBytes || entry.SHA256 == "" {
		return nil, Artifact{}, ErrArtifactChanged
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(f, info.Size()+1))
	if err := ctx.Err(); err != nil {
		return nil, Artifact{}, err
	}
	if err != nil {
		return nil, Artifact{}, err
	}
	if written != entry.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return nil, Artifact{}, ErrArtifactChanged
	}
	_, err = f.Seek(0, io.SeekStart)
	if err := ctx.Err(); err != nil {
		return nil, Artifact{}, err
	}
	if err != nil {
		return nil, Artifact{}, err
	}
	valid = true
	return f, projectArtifact(chatID, entry), nil
}
