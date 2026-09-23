package conversationexport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/documentmeta"
)

const MaxAttachmentBytes = 20 * 1024 * 1024

// BuildSnapshotAttachments materializes the current artifact manifest. The
// manifest is the allowlist and the regular files below the Chat artifact
// directory are the content authority.
func BuildSnapshotAttachments(chatID string, items []chat.ArtifactManifestItem, chatDir string) ([]AttachmentV1, error) {
	if !chat.ValidChatID(chatID) || strings.TrimSpace(chatDir) == "" {
		return nil, fmt.Errorf("invalid conversation artifact context")
	}
	latest := make(map[string]int, len(items))
	refs := make([]string, len(items))
	paths := make([]string, len(items))
	for index, item := range items {
		ref, relativePath, err := canonicalArtifactRef(chatID, item.URL)
		if err != nil {
			return nil, err
		}
		refs[index], paths[index] = ref, relativePath
		latest[ref] = index
	}

	attachments := make([]AttachmentV1, 0, len(latest))
	ids := make(map[string]string, len(latest))
	var totalBytes int64
	for index := range items {
		ref := refs[index]
		if latest[ref] != index {
			continue
		}
		segments := strings.Split(paths[index], "/")
		item := items[index]
		if item.Type != "file" || strings.TrimSpace(item.RunID) == "" || segments[1] != item.RunID {
			return nil, fmt.Errorf("published artifact manifest identity mismatch: %q", item.URL)
		}
		name := path.Base(paths[index])
		if name == "" || len(name) > 255 || strings.ContainsFunc(name, unicode.IsControl) {
			return nil, fmt.Errorf("invalid published artifact name %q", ref)
		}
		filePath, expectedFile, err := regularArtifactPath(chatDir, paths[index])
		if err != nil {
			return nil, fmt.Errorf("resolve published artifact %q: %w", ref, err)
		}
		metadata, size, fileHash, err := inspectArtifact(filePath, expectedFile, name, MaxAttachmentBytes-totalBytes)
		if err != nil {
			if errors.Is(err, ErrTooLarge) {
				return nil, newSizeLimitError(int(totalBytes+size), MaxAttachmentBytes)
			}
			return nil, fmt.Errorf("inspect published artifact %q: %w", ref, err)
		}
		totalBytes += size
		digest := sha256.Sum256([]byte(ref))
		id := hex.EncodeToString(digest[:12])
		if previous, exists := ids[id]; exists && previous != ref {
			return nil, fmt.Errorf("published artifact id collision")
		}
		ids[id] = ref
		attachments = append(attachments, AttachmentV1{
			ID: id, Name: name, MIMEType: documentmeta.NormalizeMIME(metadata.MIMEType),
			Size: size, SHA256: fileHash, SourceRef: ref,
		})
	}
	return attachments, nil
}

func canonicalArtifactRef(chatID string, raw string) (string, string, error) {
	parsedChatID, relativePath, err := chat.ParseResourceKey(chatID + "/" + strings.TrimSpace(raw))
	if err != nil || parsedChatID != chatID {
		return "", "", fmt.Errorf("invalid published artifact path %q", raw)
	}
	segments := strings.Split(relativePath, "/")
	if len(segments) != 3 || segments[0] != "artifacts" || segments[1] == "" || segments[2] == "" {
		return "", "", fmt.Errorf("published artifact must use artifacts/<runId>/<file>: %q", raw)
	}
	ref, err := chat.BuildChatScopeRef(relativePath)
	if err != nil || ref != strings.TrimSpace(raw) {
		return "", "", fmt.Errorf("published artifact path is not canonical: %q", raw)
	}
	return ref, relativePath, nil
}

func regularArtifactPath(chatDir, relativePath string) (string, os.FileInfo, error) {
	root, err := filepath.Abs(filepath.Clean(chatDir))
	if err != nil {
		return "", nil, err
	}
	current := root
	var info os.FileInfo
	for _, segment := range strings.Split(relativePath, "/") {
		current = filepath.Join(current, filepath.FromSlash(segment))
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, errors.New("symbolic links are not allowed")
		}
	}
	return current, info, nil
}

func inspectArtifact(filePath string, expectedFile os.FileInfo, semanticName string, remaining int64) (documentmeta.Metadata, int64, string, error) {
	return inspectArtifactWithReadHook(filePath, expectedFile, semanticName, remaining, nil)
}

func inspectArtifactWithReadHook(filePath string, expectedFile os.FileInfo, semanticName string, remaining int64, afterFirstRead func() error) (documentmeta.Metadata, int64, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return documentmeta.Metadata{}, 0, "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return documentmeta.Metadata{}, 0, "", errors.New("artifact is not a regular file")
	}
	if expectedFile == nil || !os.SameFile(expectedFile, before) {
		return documentmeta.Metadata{}, 0, "", errors.New("artifact changed while being read")
	}
	if before.Size() > remaining {
		return documentmeta.Metadata{}, before.Size(), "", ErrTooLarge
	}

	const sampleBytes = 512
	sampleBuffer := make([]byte, sampleBytes+utf8.UTFMax-1)
	hash := sha256.New()
	readBuffer := make([]byte, 32*1024)
	var total int64
	for {
		n, readErr := file.Read(readBuffer)
		if n > 0 {
			if total+int64(n) > remaining {
				return documentmeta.Metadata{}, total + int64(n), "", ErrTooLarge
			}
			if total < int64(len(sampleBuffer)) {
				copyEnd := min(n, len(sampleBuffer)-int(total))
				copy(sampleBuffer[int(total):], readBuffer[:copyEnd])
			}
			if _, err := hash.Write(readBuffer[:n]); err != nil {
				return documentmeta.Metadata{}, total, "", err
			}
			total += int64(n)
			if afterFirstRead != nil {
				if err := afterFirstRead(); err != nil {
					return documentmeta.Metadata{}, total, "", err
				}
				afterFirstRead = nil
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return documentmeta.Metadata{}, total, "", readErr
		}
	}
	after, err := file.Stat()
	if err != nil || total != before.Size() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		return documentmeta.Metadata{}, total, "", errors.New("artifact changed while being read")
	}
	current, err := os.Stat(filePath)
	if err != nil || !os.SameFile(after, current) || current.Size() != after.Size() ||
		!current.ModTime().Equal(after.ModTime()) {
		return documentmeta.Metadata{}, total, "", errors.New("artifact changed while being read")
	}
	sampleLength := min(int(total), sampleBytes)
	lookaheadLength := min(int(total)-sampleLength, utf8.UTFMax-1)
	sample := sampleBuffer[:sampleLength]
	lookahead := sampleBuffer[sampleLength : sampleLength+lookaheadLength]
	complete := total <= sampleBytes
	detectedMIME := documentmeta.DetectMIME(semanticName, sample, lookahead, complete)
	metadata := documentmeta.Resolve(semanticName, detectedMIME, sample, lookahead, complete)
	if strings.TrimSpace(documentmeta.NormalizeMIME(metadata.MIMEType)) == "" {
		return documentmeta.Metadata{}, total, "", errors.New("artifact MIME type is empty")
	}
	return metadata, total, hex.EncodeToString(hash.Sum(nil)), nil
}
