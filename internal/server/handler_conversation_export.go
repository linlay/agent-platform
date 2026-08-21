package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/conversationexport"
)

const (
	chatMarkdownExportFormat            = "markdown"
	chatHTMLExportFormat                = "html"
	conversationExportAssetOriginHeader = "X-Conversation-Export-Asset-Origin"
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
	if format == chatHTMLExportFormat && s.conversationHTML == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "conversation HTML export is unavailable"))
		return
	}

	snapshot, err := s.loadConversationSnapshot(chatID, time.Now().UnixMilli())
	if err != nil {
		writeConversationExportError(w, err)
		return
	}

	var body []byte
	var contentType string
	var extension string
	if format == chatHTMLExportFormat {
		body, err = s.conversationHTML.Render(
			snapshot,
			r.Header.Get(conversationExportAssetOriginHeader),
		)
		contentType = "text/html; charset=utf-8"
		extension = ".html"
	} else {
		body, err = conversationexport.RenderMarkdown(snapshot)
		contentType = "text/markdown; charset=utf-8"
		extension = ".md"
	}
	if err != nil {
		writeConversationExportError(w, err)
		return
	}

	filename := safeExportFilenameWithExtension(snapshot.Title, chatID, extension)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func parseConversationExportFormat(r *http.Request) (string, error) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", chatMarkdownExportFormat:
		return chatMarkdownExportFormat, nil
	case chatHTMLExportFormat:
		return chatHTMLExportFormat, nil
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}

func (s *Server) loadConversationSnapshot(chatID string, capturedAt int64) (conversationexport.SnapshotV1, error) {
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return conversationexport.SnapshotV1{}, err
	}
	if summary == nil {
		return conversationexport.SnapshotV1{}, chat.ErrChatNotFound
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return conversationexport.SnapshotV1{}, err
	}
	if err := validatePublicTimeContract(detail.Events); err != nil {
		return conversationexport.SnapshotV1{}, err
	}
	return conversationexport.BuildSnapshot(summary, detail.Events, capturedAt)
}

func writeConversationExportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
	case errors.Is(err, conversationexport.ErrNoRootTurn), errors.Is(err, conversationexport.ErrNoCompletedTurn), errors.Is(err, conversationexport.ErrInvalidTimeline):
		writeJSON(w, http.StatusUnprocessableEntity, api.Failure(http.StatusUnprocessableEntity, err.Error()))
	case errors.Is(err, conversationexport.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, api.Failure(http.StatusRequestEntityTooLarge, err.Error()))
	case errors.Is(err, conversationexport.ErrAssetOriginInvalid):
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
	case isTimeContractViolation(err):
		writeTimeContractViolation(w, err)
	default:
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
	}
}
