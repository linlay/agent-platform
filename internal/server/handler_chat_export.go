package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

func (s *Server) handleChatExport(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if errors.Is(err, chat.ErrChatNotFound) || summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	markdown := renderChatMarkdown(summary.ChatName, summary.AgentKey, detail.Events)
	filename := safeExportFilename(summary.ChatName, chatID)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(markdown))
}

func renderChatMarkdown(chatName string, agentKey string, events []stream.EventData) string {
	var out strings.Builder
	title := strings.TrimSpace(chatName)
	if title == "" {
		title = "Chat"
	}
	out.WriteString("# ")
	out.WriteString(markdownLine(title))
	out.WriteString("\n\n")
	if strings.TrimSpace(agentKey) != "" {
		out.WriteString("- Agent: ")
		out.WriteString(markdownLine(agentKey))
		out.WriteString("\n")
	}
	out.WriteString("- Exported: ")
	out.WriteString(time.Now().Format(time.RFC3339))
	out.WriteString("\n\n")

	for _, event := range events {
		switch event.Type {
		case "request.query":
			if hidden, _ := event.Value("hidden").(bool); hidden {
				continue
			}
			message := strings.TrimSpace(event.String("message"))
			if message == "" {
				continue
			}
			out.WriteString("## User")
			if event.Timestamp > 0 {
				out.WriteString(" - ")
				out.WriteString(time.UnixMilli(event.Timestamp).Format(time.RFC3339))
			}
			out.WriteString("\n\n")
			out.WriteString(message)
			out.WriteString("\n\n")
		case "content.snapshot":
			text := strings.TrimSpace(event.String("text"))
			if text == "" {
				continue
			}
			out.WriteString("## Assistant")
			if event.Timestamp > 0 {
				out.WriteString(" - ")
				out.WriteString(time.UnixMilli(event.Timestamp).Format(time.RFC3339))
			}
			out.WriteString("\n\n")
			out.WriteString(text)
			out.WriteString("\n\n")
		}
	}
	return out.String()
}

func markdownLine(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
}

func safeExportFilename(chatName string, chatID string) string {
	base := strings.TrimSpace(chatName)
	if base == "" {
		base = strings.TrimSpace(chatID)
	}
	if base == "" {
		base = "chat"
	}
	replacer := strings.NewReplacer("/", "_", `\`, "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_", "\n", "_", "\r", "_")
	base = strings.Trim(replacer.Replace(base), " ._")
	if base == "" {
		base = "chat"
	}
	return base + ".md"
}
