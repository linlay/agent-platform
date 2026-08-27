package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrResourceImageInvalid          = errors.New("resource image commit is invalid")
	ErrResourceImageIdentityMismatch = errors.New("resource image identity mismatch")
	ErrResourceImageRevisionConflict = errors.New("resource image revision conflict")
	ErrResourceImageOverwriteDenied  = errors.New("resource image overwrite denied")
)

type ResourceImageCommitRequest struct {
	ChatID           string
	Profile          string
	ResourceID       string
	RelativePath     string
	Mode             string
	ExpectedRevision string
	MIMEType         string
	Data             []byte
}

type ResourceImageCommitResult struct {
	ArtifactID   string `json:"artifactId"`
	ResourceID   string `json:"resourceId"`
	RelativePath string `json:"relativePath"`
	Revision     string `json:"revision"`
}

type ResourceImageCommitter interface {
	CommitResourceImage(request ResourceImageCommitRequest) (ResourceImageCommitResult, error)
}

func resourceImageRevision(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli())
}

func resourceImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

func resourceImageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func resourceImagePathMatchesMIME(relativePath string, mimeType string) bool {
	ext := strings.ToLower(filepath.Ext(relativePath))
	switch mimeType {
	case "image/png":
		return ext == ".png"
	case "image/jpeg":
		return ext == ".jpg" || ext == ".jpeg"
	case "image/webp":
		return ext == ".webp"
	default:
		return false
	}
}

func validateResourceImageSource(filePath string, relativePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 16)
	count, err := file.Read(header)
	if err != nil && count == 0 {
		return err
	}
	mimeType := resourceImageMIME(header[:count])
	if mimeType == "" || !resourceImagePathMatchesMIME(relativePath, mimeType) {
		return ErrResourceImageInvalid
	}
	return nil
}

func validateResourceImageCommit(request ResourceImageCommitRequest) error {
	if !ValidChatID(strings.TrimSpace(request.ChatID)) || strings.TrimSpace(request.ResourceID) == "" || len(request.Data) == 0 {
		return ErrResourceImageInvalid
	}
	if request.Profile != "artifact" && request.Profile != "reference" {
		return ErrResourceImageInvalid
	}
	if request.Mode != "overwrite" && request.Mode != "new-artifact" {
		return ErrResourceImageInvalid
	}
	if request.Mode == "overwrite" && request.Profile != "artifact" {
		return ErrResourceImageOverwriteDenied
	}
	segments, err := validatedResourceSegments(request.RelativePath)
	if err != nil {
		return ErrResourceImageInvalid
	}
	validProfilePath := request.Profile == "artifact" && len(segments) >= 2 && segments[0] == "artifacts"
	if request.Profile == "reference" {
		// Composer uploads keep the established root-level ChatScope contract.
		// Cross-runtime references may already be materialized under references/.
		validProfilePath = len(segments) == 1 || len(segments) >= 2 && segments[0] == "references"
	}
	if !validProfilePath || resourceImageMIME(request.Data) != request.MIMEType {
		return ErrResourceImageInvalid
	}
	if request.Mode == "overwrite" && !resourceImagePathMatchesMIME(request.RelativePath, request.MIMEType) {
		return ErrResourceImageInvalid
	}
	return nil
}

func stageResourceImage(targetDir string, pattern string, data []byte) (string, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(targetDir, pattern)
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func resourceImageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func findArtifactManifestItem(manifest ArtifactManifest, resourceID string, relativePath string) (int, bool) {
	resourceURL, err := BuildChatScopeRef(relativePath)
	if err != nil {
		return -1, false
	}
	for index := range manifest.Items {
		item := manifest.Items[index]
		if item.ArtifactID == resourceID && item.URL == resourceURL {
			return index, true
		}
	}
	return -1, false
}

func (s *FileStore) CommitResourceImage(request ResourceImageCommitRequest) (ResourceImageCommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateResourceImageCommit(request); err != nil {
		return ResourceImageCommitResult{}, err
	}
	if summary, err := s.loadSummary(request.ChatID); err != nil {
		return ResourceImageCommitResult{}, err
	} else if summary == nil {
		return ResourceImageCommitResult{}, ErrChatNotFound
	}

	chatDir := s.ChatDir(request.ChatID)
	if request.Profile == "artifact" {
		manifest, found, err := loadArtifactManifest(chatDir, request.ChatID)
		if err != nil {
			return ResourceImageCommitResult{}, err
		}
		if !found {
			return ResourceImageCommitResult{}, ErrResourceImageIdentityMismatch
		}
		if _, ok := findArtifactManifestItem(manifest, request.ResourceID, request.RelativePath); !ok {
			return ResourceImageCommitResult{}, ErrResourceImageIdentityMismatch
		}
	}
	sourcePath, err := resolveResourceInChatDir(chatDir, request.RelativePath)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	if err := validateResourceImageSource(sourcePath, request.RelativePath); err != nil {
		return ResourceImageCommitResult{}, err
	}
	if request.Mode == "overwrite" {
		return commitOverwrittenResourceImage(chatDir, request, sourcePath)
	}
	return commitNewResourceImage(chatDir, request)
}

func commitOverwrittenResourceImage(chatDir string, request ResourceImageCommitRequest, targetPath string) (ResourceImageCommitResult, error) {
	manifest, found, err := loadArtifactManifest(chatDir, request.ChatID)
	if err != nil || !found {
		if err != nil {
			return ResourceImageCommitResult{}, err
		}
		return ResourceImageCommitResult{}, ErrResourceImageIdentityMismatch
	}
	itemIndex, ok := findArtifactManifestItem(manifest, request.ResourceID, request.RelativePath)
	if !ok {
		return ResourceImageCommitResult{}, ErrResourceImageIdentityMismatch
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	if strings.TrimSpace(request.ExpectedRevision) == "" || resourceImageRevision(info) != request.ExpectedRevision {
		return ResourceImageCommitResult{}, ErrResourceImageRevisionConflict
	}

	original, err := os.ReadFile(targetPath)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	backup, err := stageResourceImage(filepath.Dir(targetPath), ".image-backup-*.tmp", original)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	defer os.Remove(backup)
	staged, err := stageResourceImage(filepath.Dir(targetPath), ".image-commit-*.tmp", request.Data)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	defer os.Remove(staged)
	if err := atomicReplaceFile(staged, targetPath); err != nil {
		return ResourceImageCommitResult{}, err
	}

	updatedInfo, err := os.Stat(targetPath)
	if err != nil {
		_ = atomicReplaceFile(backup, targetPath)
		return ResourceImageCommitResult{}, err
	}
	item := &manifest.Items[itemIndex]
	item.Name = filepath.Base(targetPath)
	item.MimeType = request.MIMEType
	item.SizeBytes = updatedInfo.Size()
	item.SHA256 = resourceImageSHA256(request.Data)
	if err := writeArtifactManifest(artifactManifestPath(chatDir), manifest); err != nil {
		if rollbackErr := atomicReplaceFile(backup, targetPath); rollbackErr != nil {
			return ResourceImageCommitResult{}, fmt.Errorf("write artifact manifest: %w; rollback image: %v", err, rollbackErr)
		}
		return ResourceImageCommitResult{}, err
	}
	return ResourceImageCommitResult{
		ArtifactID:   request.ResourceID,
		ResourceID:   request.ResourceID,
		RelativePath: request.RelativePath,
		Revision:     resourceImageRevision(updatedInfo),
	}, nil
}

func commitNewResourceImage(chatDir string, request ResourceImageCommitRequest) (ResourceImageCommitResult, error) {
	editRunID := "image-edit-" + strings.TrimPrefix(NewRunID(), "run_")
	artifactID := "artifact_" + strings.ReplaceAll(editRunID, "-", "_")
	baseName := strings.TrimSuffix(filepath.Base(request.RelativePath), filepath.Ext(request.RelativePath))
	if baseName == "" {
		baseName = "image"
	}
	fileName := baseName + "-edited" + resourceImageExtension(request.MIMEType)
	relativePath := filepath.ToSlash(filepath.Join("artifacts", editRunID, fileName))
	targetPath := filepath.Join(chatDir, filepath.FromSlash(relativePath))
	staged, err := stageResourceImage(filepath.Dir(targetPath), ".image-commit-*.tmp", request.Data)
	if err != nil {
		return ResourceImageCommitResult{}, err
	}
	defer os.Remove(staged)
	if err := atomicReplaceFile(staged, targetPath); err != nil {
		return ResourceImageCommitResult{}, err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		_ = os.Remove(targetPath)
		return ResourceImageCommitResult{}, err
	}
	resourceURL, err := BuildChatScopeRef(relativePath)
	if err != nil {
		_ = os.Remove(targetPath)
		return ResourceImageCommitResult{}, err
	}
	publishedAt := time.Now().UnixMilli()
	artifact := map[string]any{
		"artifactId": artifactID,
		"type":       "file",
		"name":       fileName,
		"mimeType":   request.MIMEType,
		"sizeBytes":  info.Size(),
		"url":        resourceURL,
		"sha256":     resourceImageSHA256(request.Data),
	}
	if err := appendArtifactManifest(chatDir, request.ChatID, editRunID, publishedAt, []map[string]any{artifact}); err != nil {
		_ = os.Remove(targetPath)
		_ = os.Remove(filepath.Dir(targetPath))
		return ResourceImageCommitResult{}, err
	}
	return ResourceImageCommitResult{
		ArtifactID:   artifactID,
		ResourceID:   artifactID,
		RelativePath: relativePath,
		Revision:     resourceImageRevision(info),
	}, nil
}
