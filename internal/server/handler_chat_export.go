package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

var errInvalidLLMTraceFile = errors.New("invalid llm trace file")

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

func (s *Server) handleChatJSONL(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}

	content, err := s.loadChatJSONLContent(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, safeJSONLFilename(chatID)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

func (s *Server) loadChatJSONLContent(chatID string) (string, error) {
	content, err := s.deps.Chats.LoadJSONLContent(chatID)
	if errors.Is(err, chat.ErrChatNotFound) && s.deps.Archives != nil {
		content, err = s.deps.Archives.LoadJSONLContent(chatID)
	}
	return content, err
}

func (s *Server) handleChatLLMTrace(w http.ResponseWriter, r *http.Request) {
	fileParam := strings.TrimSpace(r.URL.Query().Get("file"))
	if fileParam == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "file is required"))
		return
	}

	content, filename, err := s.loadChatLLMTraceContent(fileParam)
	if errors.Is(err, errInvalidLLMTraceFile) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid file"))
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "llm trace not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

func (s *Server) loadChatLLMTraceContent(fileParam string) (string, string, error) {
	relativeFile, filename, err := validateLLMTraceFileParam(fileParam)
	if err != nil {
		return "", "", err
	}
	recordDir := strings.TrimSpace(s.deps.Config.Logging.LLMInteraction.RecordDir)
	if recordDir == "" {
		return "", filename, fmt.Errorf("llm trace record dir is not configured")
	}

	base := filepath.Clean(recordDir)
	target := filepath.Join(base, filepath.FromSlash(relativeFile))
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", filename, errInvalidLLMTraceFile
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", filename, err
	}
	return string(data), filename, nil
}

func validateLLMTraceFileParam(fileParam string) (string, string, error) {
	clean := path.Clean(strings.TrimSpace(fileParam))
	if clean == "." || clean != strings.TrimSpace(fileParam) || strings.Contains(clean, "\x00") || strings.Contains(clean, "\\") || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", "", errInvalidLLMTraceFile
	}
	const prefix = "llm/"
	if !strings.HasPrefix(clean, prefix) {
		return "", "", errInvalidLLMTraceFile
	}
	filename := strings.TrimPrefix(clean, prefix)
	if filename == "" || strings.Contains(filename, "/") || !strings.HasSuffix(filename, ".json") {
		return "", "", errInvalidLLMTraceFile
	}
	stem := strings.TrimSuffix(filename, ".json")
	if len(stem) < 5 || stem[len(stem)-4] != '_' {
		return "", "", errInvalidLLMTraceFile
	}
	name := stem[:len(stem)-4]
	seq := stem[len(stem)-3:]
	if !isSafeLLMTraceName(name) || !isThreeDigitSequence(seq) {
		return "", "", errInvalidLLMTraceFile
	}
	return clean, filename, nil
}

func isSafeLLMTraceName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func isThreeDigitSequence(seq string) bool {
	if len(seq) != 3 {
		return false
	}
	for i := 0; i < len(seq); i++ {
		if seq[i] < '0' || seq[i] > '9' {
			return false
		}
	}
	return true
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
			if !api.QueryRoleVisible(event.String("role")) {
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

func safeJSONLFilename(chatID string) string {
	base := strings.TrimSpace(chatID)
	if base == "" {
		base = "chat"
	}
	replacer := strings.NewReplacer("/", "_", `\`, "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_", "\n", "_", "\r", "_")
	base = strings.Trim(replacer.Replace(base), " ._")
	if base == "" {
		base = "chat"
	}
	return base + ".jsonl"
}
