package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrResourceDocumentInvalid          = errors.New("resource document commit is invalid")
	ErrResourceDocumentIdentityMismatch = errors.New("resource document identity mismatch")
	ErrResourceDocumentRevisionConflict = errors.New("resource document revision conflict")
	ErrResourceDocumentOverwriteDenied  = errors.New("resource document overwrite denied")
)

type ResourceDocumentCommitRequest struct {
	ChatID           string
	Profile          string
	ResourceID       string
	RelativePath     string
	Mode             string
	ExpectedRevision string
	DocumentKind     string
	MIMEType         string
	Data             []byte
}

type ResourceDocumentCommitResult struct {
	ArtifactID   string `json:"artifactId"`
	ResourceID   string `json:"resourceId"`
	RelativePath string `json:"relativePath"`
	Revision     string `json:"revision"`
}

type ResourceDocumentCommitter interface {
	CommitResourceDocument(request ResourceDocumentCommitRequest) (ResourceDocumentCommitResult, error)
}

func validEditableTextDocument(kind string, mimeType string, data []byte) bool {
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return false
	}
	switch kind {
	case "document-html":
		return mimeType == "text/html" || mimeType == "application/xhtml+xml"
	case "document-markdown":
		return mimeType == "text/markdown" || mimeType == "text/plain"
	case "document-text", "document-code":
		return strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "json") ||
			strings.Contains(mimeType, "xml") || strings.Contains(mimeType, "javascript") || strings.Contains(mimeType, "yaml")
	default:
		return false
	}
}

func resourceTextDocumentKind(relativePath string) string {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(relativePath)))
	switch extension {
	case ".html", ".htm", ".xhtml":
		return "document-html"
	case ".md", ".markdown", ".mdx":
		return "document-markdown"
	case ".c", ".cc", ".cpp", ".css", ".go", ".h", ".hpp", ".ini", ".java",
		".js", ".json", ".jsx", ".mjs", ".py", ".rb", ".rs", ".sh", ".sql",
		".toml", ".ts", ".tsx", ".xml", ".yaml", ".yml":
		return "document-code"
	case ".apng", ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jpeg",
		".jpg", ".png", ".svg", ".tif", ".tiff", ".webp":
		return "document-image"
	default:
		return "document-text"
	}
}

func validResourceDocumentPayload(request ResourceDocumentCommitRequest) bool {
	if request.DocumentKind == "document-image" {
		return resourceImageMIME(request.Data) == request.MIMEType && resourceImagePathMatchesMIME(request.RelativePath, request.MIMEType)
	}
	return resourceTextDocumentKind(request.RelativePath) == request.DocumentKind &&
		validEditableTextDocument(request.DocumentKind, request.MIMEType, request.Data)
}

func validateResourceDocumentCommit(request ResourceDocumentCommitRequest) error {
	if !ValidChatID(strings.TrimSpace(request.ChatID)) || strings.TrimSpace(request.ResourceID) == "" || len(request.Data) == 0 && request.DocumentKind == "document-image" {
		return ErrResourceDocumentInvalid
	}
	if request.Profile != "artifact" && request.Profile != "reference" {
		return ErrResourceDocumentInvalid
	}
	if request.Mode != "overwrite" && request.Mode != "new-artifact" {
		return ErrResourceDocumentInvalid
	}
	if request.Mode == "overwrite" && request.Profile != "artifact" {
		return ErrResourceDocumentOverwriteDenied
	}
	segments, err := validatedResourceSegments(request.RelativePath)
	if err != nil {
		return ErrResourceDocumentInvalid
	}
	validProfilePath := request.Profile == "artifact" && len(segments) >= 2 && segments[0] == "artifacts"
	if request.Profile == "reference" {
		validProfilePath = len(segments) == 1 || len(segments) >= 2 && segments[0] == "references"
	}
	if !validProfilePath || !validResourceDocumentPayload(request) {
		return ErrResourceDocumentInvalid
	}
	return nil
}

func validateResourceDocumentSource(path string, request ResourceDocumentCommitRequest) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrResourceDocumentInvalid
	}
	if request.DocumentKind == "document-image" {
		return validateResourceImageSource(path, request.RelativePath)
	}
	if resourceTextDocumentKind(request.RelativePath) != request.DocumentKind {
		return ErrResourceDocumentInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return ErrResourceDocumentInvalid
	}
	return nil
}

func (s *FileStore) CommitResourceDocument(request ResourceDocumentCommitRequest) (ResourceDocumentCommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateResourceDocumentCommit(request); err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	if summary, err := s.loadSummary(request.ChatID); err != nil {
		return ResourceDocumentCommitResult{}, err
	} else if summary == nil {
		return ResourceDocumentCommitResult{}, ErrChatNotFound
	}
	chatDir := s.ChatDir(request.ChatID)
	if request.Profile == "artifact" {
		manifest, found, err := loadArtifactManifest(chatDir, request.ChatID)
		if err != nil {
			return ResourceDocumentCommitResult{}, err
		}
		if !found {
			return ResourceDocumentCommitResult{}, ErrResourceDocumentIdentityMismatch
		}
		if _, ok := findArtifactManifestItem(manifest, request.ResourceID, request.RelativePath); !ok {
			return ResourceDocumentCommitResult{}, ErrResourceDocumentIdentityMismatch
		}
	}
	sourcePath, err := resolveResourceInChatDir(chatDir, request.RelativePath)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	if err := validateResourceDocumentSource(sourcePath, request); err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	if strings.TrimSpace(request.ExpectedRevision) == "" || resourceImageRevision(sourceInfo) != request.ExpectedRevision {
		return ResourceDocumentCommitResult{}, ErrResourceDocumentRevisionConflict
	}
	if request.Mode == "overwrite" {
		return commitOverwrittenResourceDocument(chatDir, request, sourcePath)
	}
	return commitNewResourceDocument(chatDir, request)
}

func commitOverwrittenResourceDocument(chatDir string, request ResourceDocumentCommitRequest, targetPath string) (ResourceDocumentCommitResult, error) {
	manifest, found, err := loadArtifactManifest(chatDir, request.ChatID)
	if err != nil || !found {
		if err != nil {
			return ResourceDocumentCommitResult{}, err
		}
		return ResourceDocumentCommitResult{}, ErrResourceDocumentIdentityMismatch
	}
	itemIndex, ok := findArtifactManifestItem(manifest, request.ResourceID, request.RelativePath)
	if !ok {
		return ResourceDocumentCommitResult{}, ErrResourceDocumentIdentityMismatch
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	if strings.TrimSpace(request.ExpectedRevision) == "" || resourceImageRevision(info) != request.ExpectedRevision {
		return ResourceDocumentCommitResult{}, ErrResourceDocumentRevisionConflict
	}
	original, err := os.ReadFile(targetPath)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	backup, err := stageResourceImage(filepath.Dir(targetPath), ".document-backup-*.tmp", original)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	defer os.Remove(backup)
	staged, err := stageResourceImage(filepath.Dir(targetPath), ".document-commit-*.tmp", request.Data)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	defer os.Remove(staged)
	if err := atomicReplaceFile(staged, targetPath); err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	updatedInfo, err := os.Stat(targetPath)
	if err != nil {
		_ = atomicReplaceFile(backup, targetPath)
		return ResourceDocumentCommitResult{}, err
	}
	item := &manifest.Items[itemIndex]
	item.Name = filepath.Base(targetPath)
	item.MimeType = request.MIMEType
	item.SizeBytes = updatedInfo.Size()
	item.SHA256 = resourceImageSHA256(request.Data)
	if err := writeArtifactManifest(artifactManifestPath(chatDir), manifest); err != nil {
		if rollbackErr := atomicReplaceFile(backup, targetPath); rollbackErr != nil {
			return ResourceDocumentCommitResult{}, fmt.Errorf("write artifact manifest: %w; rollback document: %v", err, rollbackErr)
		}
		return ResourceDocumentCommitResult{}, err
	}
	return ResourceDocumentCommitResult{
		ArtifactID: request.ResourceID, ResourceID: request.ResourceID,
		RelativePath: request.RelativePath, Revision: resourceImageRevision(updatedInfo),
	}, nil
}

func commitNewResourceDocument(chatDir string, request ResourceDocumentCommitRequest) (ResourceDocumentCommitResult, error) {
	editRunID := "document-edit-" + strings.TrimPrefix(NewRunID(), "run_")
	artifactID := "artifact_" + strings.ReplaceAll(editRunID, "-", "_")
	extension := filepath.Ext(request.RelativePath)
	baseName := strings.TrimSuffix(filepath.Base(request.RelativePath), extension)
	if baseName == "" {
		baseName = "document"
	}
	if request.DocumentKind == "document-image" {
		extension = resourceImageExtension(request.MIMEType)
	}
	fileName := baseName + "-edited" + extension
	relativePath := filepath.ToSlash(filepath.Join("artifacts", editRunID, fileName))
	targetPath := filepath.Join(chatDir, filepath.FromSlash(relativePath))
	staged, err := stageResourceImage(filepath.Dir(targetPath), ".document-commit-*.tmp", request.Data)
	if err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	defer os.Remove(staged)
	if err := atomicReplaceFile(staged, targetPath); err != nil {
		return ResourceDocumentCommitResult{}, err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		_ = os.Remove(targetPath)
		return ResourceDocumentCommitResult{}, err
	}
	resourceURL, err := BuildChatScopeRef(relativePath)
	if err != nil {
		_ = os.Remove(targetPath)
		return ResourceDocumentCommitResult{}, err
	}
	artifact := map[string]any{
		"artifactId": artifactID, "type": "file", "name": fileName,
		"mimeType": request.MIMEType, "sizeBytes": info.Size(),
		"url": resourceURL, "sha256": resourceImageSHA256(request.Data),
	}
	if err := appendArtifactManifest(chatDir, request.ChatID, editRunID, time.Now().UnixMilli(), []map[string]any{artifact}); err != nil {
		_ = os.Remove(targetPath)
		_ = os.Remove(filepath.Dir(targetPath))
		return ResourceDocumentCommitResult{}, err
	}
	return ResourceDocumentCommitResult{
		ArtifactID: artifactID, ResourceID: artifactID,
		RelativePath: relativePath, Revision: resourceImageRevision(info),
	}, nil
}
