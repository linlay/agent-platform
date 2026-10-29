package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/conversationexport"
	"agent-platform/internal/i18n"
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
	contentType, extension := "text/markdown; charset=utf-8", ".md"
	if format == chatSnapshotExportFormat {
		contentType, extension = "application/json; charset=utf-8", ".snapshot.json"
	}
	document, err := s.loadConversationSnapshot(r.Context(), chatID, format == chatSnapshotExportFormat, time.Now().UnixMilli(), requestLocale(r, i18n.LocaleZhCN))
	if err == nil && len(document.JSON) > s.deps.Config.Export.MaxSnapshotBytes {
		err = &conversationexport.SizeLimitError{Actual: len(document.JSON), Limit: s.deps.Config.Export.MaxSnapshotBytes}
	}
	body := document.JSON
	if err == nil && format == chatMarkdownExportFormat {
		body, err = conversationexport.RenderMarkdown(document.Snapshot)
	}
	if err != nil {
		writeConversationExportError(w, err)
		return
	}
	filename := safeExportFilenameWithExtension(document.Snapshot.Title, chatID, extension)
	if format == chatSnapshotExportFormat {
		w.Header().Set("X-Conversation-Export-Max-Bytes", strconv.Itoa(s.deps.Config.Export.MaxSnapshotBytes))
		if s.deps.Config.CORS.Enabled {
			w.Header().Add("Access-Control-Expose-Headers", "X-Conversation-Export-Max-Bytes")
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) loadConversationSnapshot(ctx context.Context, chatID string, includeResources bool, capturedAt int64, locale string) (conversationexport.SnapshotDocument, error) {
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
	s.enrichToolMetadata(detail.Events, summaryAgentKey(summary))
	for index := range detail.Events {
		detail.Events[index] = localizeStreamEventData(locale, detail.Events[index])
	}
	var files *chatresource.Export
	if includeResources {
		files = s.chatResources.Export(chatID)
		if files.Err != nil && !errors.Is(files.Err, chatresource.ErrNotConfigured) {
			log.Printf("[chat] snapshot artifact manifest unavailable chatId=%s: %v", chatID, files.Err)
		}
	}
	return conversationexport.BuildDocument(ctx, summary, detail.Events, capturedAt, locale, s.resolveExportAssistant, files)
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

func (s *Server) resolveExportAssistant(agentKey string) *conversationexport.AssistantV1 {
	if s.deps.Registry == nil {
		return nil
	}
	var name, mode string
	var icon any
	if agentKey != "" {
		definition, ok := s.deps.Registry.AgentDefinition(agentKey)
		if !ok {
			return nil
		}
		name, icon, mode = definition.Name, definition.Icon, definition.Mode
	} else {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > conversationexport.MaxTitleBytes {
		return nil
	}
	assistant := &conversationexport.AssistantV1{Name: name, Mode: catalog.AgentModeForAPI(mode)}
	if descriptor, ok := icon.(map[string]any); ok {
		if color, ok := descriptor["color"].(string); ok && len(color) <= 128 {
			assistant.IconColor = strings.TrimSpace(color)
		}
		if iconName, ok := descriptor["name"].(string); ok && validExportIconName(iconName) {
			assistant.IconName = iconName
		}
	}
	return assistant
}

func validExportIconName(value string) bool {
	if len(value) == 0 || len(value) > 40 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
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
