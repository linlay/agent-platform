package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrResourceImageInvalid = errors.New("resource image commit is invalid")

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
