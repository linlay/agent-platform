package server

import (
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/conversationexport"
	"agent-platform/internal/i18n"
)

const (
	chatMarkdownExportFormat = "markdown"
	chatSnapshotExportFormat = "snapshot"
)

type publishedArtifactReader interface {
	PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
}

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
	locale := requestLocale(r, i18n.LocaleZhCN)
	var attachments []conversationexport.AttachmentV1
	if format == chatSnapshotExportFormat {
		reader, ok := s.deps.Chats.(publishedArtifactReader)
		if ok {
			if items, readErr := reader.PublishedArtifacts(chatID); readErr == nil {
				var skipped int
				attachments, skipped = conversationexport.BuildSnapshotAttachments(chatID, items)
				if skipped > 0 {
					log.Printf("[chat] snapshot omitted %d invalid artifacts chatId=%s", skipped, chatID)
				}
			} else {
				log.Printf("[chat] snapshot artifact manifest unavailable chatId=%s: %v", chatID, readErr)
			}
		}
		contentType = "application/json; charset=utf-8"
		extension = ".snapshot.json"
	} else {
		contentType = "text/markdown; charset=utf-8"
		extension = ".md"
	}
	var document conversationexport.SnapshotDocument
	if err == nil {
		document, err = s.loadConversationSnapshot(chatID, attachments, time.Now().UnixMilli(), locale)
	}
	if err == nil {
		title = document.Snapshot.Title
		if format == chatSnapshotExportFormat {
			body = document.JSON
		} else {
			body, err = conversationexport.RenderMarkdown(document.Snapshot)
		}
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

func (s *Server) loadConversationSnapshot(chatID string, attachments []conversationexport.AttachmentV1, capturedAt int64, locale string) (conversationexport.SnapshotDocument, error) {
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
	return conversationexport.BuildSnapshotDocument(summary, detail.Events, attachments, capturedAt, locale, s.resolveExportAssistant)
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
	var name string
	var icon any
	if agentKey != "" {
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
