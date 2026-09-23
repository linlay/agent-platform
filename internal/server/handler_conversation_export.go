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
	var body []byte
	var contentType string
	var extension string
	var title string
	locale := "zh-CN"
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		locale = "en-US"
	}
	exporter := conversationexport.Service{Chats: s.deps.Chats, ResolveAssistant: s.resolveExportAssistant}
	if format == chatSnapshotExportFormat {
		var document conversationexport.SnapshotDocument
		document, err = exporter.Snapshot(chatID, time.Now().UnixMilli(), locale)
		if err == nil {
			body, title = document.JSON, document.Snapshot.Title
		}
		contentType = "application/json; charset=utf-8"
		extension = ".snapshot.json"
	} else {
		body, title, err = exporter.Markdown(chatID, time.Now().UnixMilli(), locale)
		contentType = "text/markdown; charset=utf-8"
		extension = ".md"
	}
	if err != nil {
		writeConversationExportError(w, err)
		return
	}

	filename := safeExportFilenameWithExtension(title, chatID, extension)
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

func (s *Server) resolveExportAssistant(agentKey, teamID string) *conversationexport.AssistantV1 {
	if s.deps.Registry == nil {
		return nil
	}
	var name string
	var icon any
	if teamID != "" {
		definition, ok := s.deps.Registry.TeamDefinition(teamID)
		if !ok {
			return nil
		}
		name, icon = definition.Name, definition.Icon
	} else if agentKey != "" {
		definition, ok := s.deps.Registry.AgentDefinition(agentKey)
		if !ok {
			return nil
		}
		name, icon = definition.Name, definition.Icon
	} else {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > conversationexport.MaxTitleBytes {
		return nil
	}
	assistant := &conversationexport.AssistantV1{Name: name}
	if descriptor, ok := icon.(map[string]any); ok {
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
