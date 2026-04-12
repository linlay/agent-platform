package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/api"
)

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
	if s.deps.Config.ChatImage.ResourceTicketEnabled {
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
	_, _, err := s.deps.Chats.EnsureChat(chatID, s.deps.Registry.DefaultAgentKey(), "", r.FormValue("name"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	file, header, err := pickUploadFile(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()

	uploadID := "r01"
	targetName := safeFilename(header.Filename)
	targetPath := filepath.Join(s.deps.Chats.ChatDir(chatID), targetName)
	sum, size, err := saveUploadedFile(targetPath, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	resourceURL := "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(filepath.Join(chatID, targetName)))
	writeJSON(w, http.StatusOK, api.Success(api.UploadResponse{
		RequestID: requestID,
		ChatID:    chatID,
		Upload: api.UploadTicket{
			ID:        uploadID,
			Type:      "file",
			Name:      targetName,
			MimeType:  header.Header.Get("Content-Type"),
			SizeBytes: size,
			URL:       resourceURL,
			SHA256:    sum,
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

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	return name
}
