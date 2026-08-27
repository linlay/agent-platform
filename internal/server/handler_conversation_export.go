package server

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/conversationexport"
)

const (
	chatMarkdownExportFormat = "markdown"
	chatSnapshotExportFormat = "snapshot"
)

func (s *Server) handleChatExport(w http.ResponseWriter, r *http.Request) {
	format, err := parseConversationExportFormat(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}
	document, err := s.loadConversationSnapshot(chatID, time.Now().UnixMilli())
	if err != nil {
		writeConversationExportError(w, err)
		return
	}

	var body []byte
	var contentType string
	var extension string
	if format == chatSnapshotExportFormat {
		body = document.JSON
		contentType = "application/json; charset=utf-8"
		extension = ".snapshot.json"
	} else {
		body, err = conversationexport.RenderMarkdown(document.Snapshot)
		contentType = "text/markdown; charset=utf-8"
		extension = ".md"
	}
	if err != nil {
		writeConversationExportError(w, err)
		return
	}

	filename := safeExportFilenameWithExtension(document.Snapshot.Title, chatID, extension)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func parseConversationExportFormat(r *http.Request) (string, error) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", chatMarkdownExportFormat:
		return chatMarkdownExportFormat, nil
	case chatSnapshotExportFormat:
		return chatSnapshotExportFormat, nil
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}

func (s *Server) loadConversationSnapshot(chatID string, capturedAt int64) (conversationexport.SnapshotDocument, error) {
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return conversationexport.SnapshotDocument{}, err
	}
	if summary == nil {
		return conversationexport.SnapshotDocument{}, chat.ErrChatNotFound
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return conversationexport.SnapshotDocument{}, err
	}
	if err := validatePublicTimeContract(detail.Events); err != nil {
		return conversationexport.SnapshotDocument{}, err
	}
	return conversationexport.BuildSnapshotDocument(summary, detail.Events, capturedAt)
}

func writeConversationExportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
	case errors.Is(err, conversationexport.ErrNoRootTurn), errors.Is(err, conversationexport.ErrNoCompletedTurn), errors.Is(err, conversationexport.ErrInvalidTimeline):
		writeJSON(w, http.StatusUnprocessableEntity, api.Failure(http.StatusUnprocessableEntity, err.Error()))
	case errors.Is(err, conversationexport.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, api.Failure(http.StatusRequestEntityTooLarge, err.Error()))
	case isTimeContractViolation(err):
		writeTimeContractViolation(w, err)
	default:
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
	}
}
