package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

const uploadManifestName = ".uploads.jsonl"

type uploadManifestEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

func (s *Server) handleViewport(w http.ResponseWriter, r *http.Request) {
	viewportKey := r.URL.Query().Get("viewportKey")
	if viewportKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "viewportKey is required"))
		return
	}
	payload, err := s.deps.Viewport.Get(r.Context(), viewportKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(payload))
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	fileParam := r.URL.Query().Get("file")
	if fileParam == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "file is required"))
		return
	}
	if s.deps.Config.ResourceTicket.Enabled() {
		principal := PrincipalFromContext(r.Context())
		ticket := strings.TrimSpace(r.URL.Query().Get("t"))
		if principal == nil {
			if ticket == "" {
				writeJSON(w, http.StatusUnauthorized, api.Failure(http.StatusUnauthorized, "resource ticket required"))
				return
			}
			chatID, err := s.ticketService.Verify(ticket)
			if err != nil {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, err.Error()))
				return
			}
			if !resourceBelongsToChat(fileParam, chatID) {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket chat mismatch"))
				return
			}
		}
	}
	path, err := s.deps.Chats.ResolveResource(fileParam)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
			return
		}
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid multipart form"))
		return
	}
	requestID := strings.TrimSpace(r.FormValue("requestId"))
	if requestID == "" {
		requestID = newRunID()
	}
	chatID := strings.TrimSpace(r.FormValue("chatId"))
	if chatID == "" {
		chatID = newChatID()
	}
	agentKey := strings.TrimSpace(r.FormValue("agentKey"))
	summary, created, err := s.deps.Chats.EnsureChat(chatID, agentKey, "", r.FormValue("name"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if created {
		s.broadcast("chat.created", map[string]any{
			"chatId":    chatID,
			"chatName":  summary.ChatName,
			"agentKey":  agentKey,
			"timestamp": summary.CreatedAt,
		})
	}
	file, header, err := pickUploadFile(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()

	targetName := safeFilename(header.Filename)
	uploadID, err := s.allocateUploadID(chatID, targetName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	targetPath := filepath.Join(s.deps.Chats.ChatDir(chatID), targetName)
	sum, size, err := saveUploadedFile(targetPath, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	resourceURL := "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(filepath.Join(chatID, targetName)))
	sandboxPath := "/workspace/" + filepath.ToSlash(targetName)
	writeJSON(w, http.StatusOK, api.Success(api.UploadResponse{
		RequestID: requestID,
		ChatID:    chatID,
		Upload: api.UploadTicket{
			ID:          uploadID,
			Type:        "file",
			Name:        targetName,
			MimeType:    header.Header.Get("Content-Type"),
			SizeBytes:   size,
			URL:         resourceURL,
			SHA256:      sum,
			SandboxPath: sandboxPath,
		},
	}))
}

func pickUploadFile(form *multipart.Form) (multipart.File, *multipart.FileHeader, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil, errors.New("file is required")
	}
	for _, headers := range form.File {
		if len(headers) == 0 {
			continue
		}
		file, err := headers[0].Open()
		return file, headers[0], err
	}
	return nil, nil, errors.New("file is required")
}

func saveUploadedFile(path string, src multipart.File) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	size, err := io.Copy(writer, src)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (s *Server) allocateUploadID(chatID string, name string) (string, error) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()

	chatDir := s.deps.Chats.ChatDir(chatID)
	next, err := nextUploadSequence(chatDir)
	if err != nil {
		return "", err
	}
	entry := uploadManifestEntry{
		ID:        fmt.Sprintf("r%02d", next),
		Name:      name,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := appendUploadManifestEntry(chatDir, entry); err != nil {
		return "", err
	}
	return entry.ID, nil
}

func nextUploadSequence(chatDir string) (int, error) {
	manifestPath := filepath.Join(chatDir, uploadManifestName)
	if _, err := os.Stat(manifestPath); err == nil {
		maxID, err := maxUploadSequenceFromManifest(manifestPath)
		if err != nil {
			return 0, err
		}
		return maxID + 1, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	count, err := countExistingRootUploads(chatDir)
	if err != nil {
		return 0, err
	}
	return count + 1, nil
}

func maxUploadSequenceFromManifest(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	maxID := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry uploadManifestEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return 0, err
		}
		if sequence := uploadIDSequence(entry.ID); sequence > maxID {
			maxID = sequence
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return maxID, nil
}

func uploadIDSequence(id string) int {
	if !strings.HasPrefix(id, "r") {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimPrefix(id, "r"))
	if err != nil || value < 1 {
		return 0
	}
	return value
}

func countExistingRootUploads(chatDir string) (int, error) {
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || isUploadMetadataFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.Mode().IsRegular() {
			count++
		}
	}
	return count, nil
}

func isUploadMetadataFile(name string) bool {
	switch name {
	case uploadManifestName, "events.jsonl", "raw_messages.jsonl":
		return true
	default:
		return false
	}
}

func appendUploadManifestEntry(chatDir string, entry uploadManifestEntry) error {
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(chatDir, uploadManifestName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	return name
}
