package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

var errInvalidLLMTraceFile = errors.New("invalid llm trace file")

func (s *Server) handleChatExport(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if err := validatePublicTimeContract(detail.Events); err != nil {
		writeTimeContractViolation(w, err)
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
	if err != nil {
		return "", err
	}
	if err := validatePersistedJSONLTimeContract(content, "chat.jsonl"); err != nil {
		return "", err
	}
	return content, err
}

func (s *Server) handleChatSystemPrompt(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}

	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if runID == "" || agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and agentKey are required"))
		return
	}

	snapshot, err := s.deps.Chats.LoadRunSystemInit(chatID, runID, agentKey)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if snapshot == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "system prompt not found"))
		return
	}

	writeJSON(w, http.StatusOK, api.Success(api.ChatSystemPromptResponse{
		ChatID:   chatID,
		RunID:    runID,
		AgentKey: agentKey,
		SystemRef: api.ChatSystemPromptRef{
			AgentKey:    snapshot.AgentKey,
			CacheKey:    snapshot.CacheKey,
			Fingerprint: snapshot.Fingerprint,
		},
		SystemMessage: snapshot.SystemMessage,
	}))
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
	if err := validatePersistedTraceTimeContract(data, "llm_trace"); err != nil {
		return "", filename, err
	}
	return string(data), filename, nil
}

// validatePersistedJSONLTimeContract intentionally only reads old content. It
// never normalizes, rewrites, or fills a timestamp: incompatible historical
// records must be surfaced to the caller as a 422 rather than silently made
// to look current.
func validatePersistedJSONLTimeContract(content string, baseLocation string) error {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	for index := 0; ; index++ {
		var line map[string]any
		err := decoder.Decode(&line)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse persisted JSONL: %w", err)
		}
		location := fmt.Sprintf("%s[%d]", baseLocation, index)
		lineType, _ := line["_type"].(string)
		switch strings.TrimSpace(lineType) {
		case "query", "react", "react-tool", "plan-execute", "step", "event", "submit", "steer", chat.CompactCheckpointLineType, chat.ToolCompactLineType:
			if err := validateRequiredJSONEpochMillis(line, "updatedAt", location); err != nil {
				return err
			}
		case "system-init":
			if err := validateRequiredJSONEpochMillis(line, "createdAt", location); err != nil {
				return err
			}
		}
		if strings.TrimSpace(lineType) == "event" {
			event, ok := line["event"].(map[string]any)
			if !ok {
				return &timecontract.Violation{Field: "timestamp", Location: location + ".event.timestamp", Reason: "event payload is required"}
			}
			if err := validateRequiredJSONEpochMillis(event, "timestamp", location+".event"); err != nil {
				return err
			}
		}
		if err := validatePersistedJSONLMessages(line["messages"], location+".messages"); err != nil {
			return err
		}
	}
}

func validatePersistedJSONLMessages(raw any, location string) error {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || len(item) == 0 {
			continue
		}
		if err := validateRequiredJSONEpochMillis(item, "ts", fmt.Sprintf("%s[%d]", location, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredJSONEpochMillis(object map[string]any, field string, location string) error {
	value, ok := object[field]
	if !ok {
		return &timecontract.Violation{Field: field, Location: location + "." + field, Reason: "is required"}
	}
	_, err := timecontract.ParseEpochMillis(value, field, location+"."+field)
	return err
}

func validatePersistedTraceTimeContract(data []byte, location string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("parse persisted llm trace: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("parse persisted llm trace: multiple JSON values")
		}
		return fmt.Errorf("parse persisted llm trace: %w", err)
	}
	trace, ok := payload.(map[string]any)
	if !ok {
		return &timecontract.Violation{Field: "sentAt", Location: location, Reason: "trace must be a JSON object"}
	}
	for _, field := range []string{"sentAt", "responseStartedAt", "completedAt"} {
		if _, exists := trace[field]; exists {
			if err := requireTraceTimePair(trace, field, location); err != nil {
				return err
			}
		}
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(trace["status"])))
	finalized := status == "ok" || status == "error" || status == "interrupted" || trace["completedAt"] != nil
	if finalized {
		if err := requireTraceTimePair(trace, "sentAt", location); err != nil {
			return err
		}
		if err := requireTraceTimePair(trace, "completedAt", location); err != nil {
			return err
		}
	}
	if status == "ok" {
		if err := requireTraceTimePair(trace, "responseStartedAt", location); err != nil {
			return err
		}
	}
	if rawInterrupt, exists := trace["interrupt"]; exists {
		interrupt, ok := rawInterrupt.(map[string]any)
		if !ok {
			return &timecontract.Violation{Field: "interruptedAt", Location: location + ".interrupt", Reason: "interrupt must be an object"}
		}
		if err := requireTraceTimePair(interrupt, "interruptedAt", location+".interrupt"); err != nil {
			return err
		}
	}
	return nil
}

func requireTraceTimePair(payload map[string]any, atField string, location string) error {
	value, ok := payload[atField]
	if !ok {
		return &timecontract.Violation{Field: atField, Location: location + "." + atField, Reason: "is required"}
	}
	millis, err := timecontract.ParseEpochMillis(value, atField, location+"."+atField)
	if err != nil {
		return err
	}
	timeField := strings.TrimSuffix(atField, "At") + "Time"
	raw, ok := payload[timeField].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return &timecontract.Violation{Field: timeField, Location: location + "." + timeField, Reason: "is required"}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !hasTraceRFC3339Offset(raw) {
		return &timecontract.Violation{Field: timeField, Location: location + "." + timeField, Reason: "must be RFC3339/RFC3339Nano with Z or offset"}
	}
	if parsed.Nanosecond()%int(time.Millisecond) != 0 || parsed.UnixMilli() != millis {
		return &timecontract.Violation{Field: timeField, Location: location + "." + timeField, Reason: "must represent the same instant as " + atField}
	}
	return nil
}

func hasTraceRFC3339Offset(value string) bool {
	if strings.HasSuffix(value, "Z") {
		return true
	}
	if len(value) < len("+00:00") {
		return false
	}
	offset := value[len(value)-6:]
	return (offset[0] == '+' || offset[0] == '-') && offset[3] == ':'
}

func validateLLMTraceFileParam(fileParam string) (string, string, error) {
	clean := path.Clean(strings.TrimSpace(fileParam))
	if clean == "." || clean != strings.TrimSpace(fileParam) || strings.Contains(clean, "\x00") || strings.Contains(clean, "\\") || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", "", errInvalidLLMTraceFile
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 3 || parts[1] != ".llm-records" {
		return "", "", errInvalidLLMTraceFile
	}
	chatID := parts[0]
	filename := parts[2]
	if !chat.ValidChatID(chatID) || filename == "" || strings.Contains(filename, "/") || !strings.HasSuffix(filename, ".json") {
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
