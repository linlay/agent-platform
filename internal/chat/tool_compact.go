package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultToolCompactKeepRecent = 5
	ToolCompactClearedMessage    = "[Old tool result content cleared]"
)

var defaultToolCompactableTools = map[string]struct{}{
	"file_read":    {},
	"bash":         {},
	"bash_sandbox": {},
	"file_grep":    {},
	"file_glob":    {},
	"file_edit":    {},
	"file_write":   {},
}

type ToolCompactSnapshot struct {
	ChatID                     string
	FileHash                   string
	ToolsCleared               int
	ToolsKept                  int
	TokensFreed                int
	PreCompactEstimatedTokens  int
	PostCompactEstimatedTokens int
	CompressionRatio           float64
	replacements               []toolCompactReplacement
}

type toolCompactReplacement struct {
	LineIndex    int
	MessageIndex int
}

type toolCompactCandidate struct {
	LineIndex      int
	MessageIndex   int
	ToolID         string
	ToolName       string
	Content        string
	AlreadyCleared bool
}

func (s *FileStore) BuildToolCompactSnapshot(chatID string, keepRecent int) (ToolCompactSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return ToolCompactSnapshot{}, os.ErrPermission
	}
	sum, err := s.loadSummary(chatID)
	if err != nil {
		return ToolCompactSnapshot{}, err
	}
	if sum == nil {
		return ToolCompactSnapshot{}, ErrChatNotFound
	}
	if keepRecent <= 0 {
		keepRecent = DefaultToolCompactKeepRecent
	}

	records, data, err := readJSONLineRecords(s.chatJSONLPath(chatID))
	if err != nil {
		return ToolCompactSnapshot{}, err
	}
	if len(records) == 0 {
		return ToolCompactSnapshot{}, ErrNoCompactableHistory
	}

	candidates := collectToolCompactCandidates(records)
	if len(candidates) == 0 {
		return ToolCompactSnapshot{
			ChatID:   chatID,
			FileHash: jsonlContentHash(data),
		}, nil
	}

	keepStart := len(candidates) - keepRecent
	if keepStart < 0 {
		keepStart = 0
	}
	toolsKept := len(candidates) - keepStart
	clearedTokenCost := EstimateTextTokens(ToolCompactClearedMessage)
	replacements := make([]toolCompactReplacement, 0, keepStart)
	tokensFreed := 0
	for _, candidate := range candidates[:keepStart] {
		if candidate.AlreadyCleared || strings.TrimSpace(candidate.Content) == "" {
			continue
		}
		replacements = append(replacements, toolCompactReplacement{
			LineIndex:    candidate.LineIndex,
			MessageIndex: candidate.MessageIndex,
		})
		freed := EstimateTextTokens(candidate.Content) - clearedTokenCost
		if freed > 0 {
			tokensFreed += freed
		}
	}

	preTokens := EstimateRawMessageTokens(rawMessagesFromJSONLLines(recordValues(records)))
	postTokens := preTokens - tokensFreed
	if postTokens < 0 {
		postTokens = 0
	}
	ratio := 0.0
	if preTokens > 0 {
		ratio = float64(postTokens) / float64(preTokens)
	}

	return ToolCompactSnapshot{
		ChatID:                     chatID,
		FileHash:                   jsonlContentHash(data),
		ToolsCleared:               len(replacements),
		ToolsKept:                  toolsKept,
		TokensFreed:                tokensFreed,
		PreCompactEstimatedTokens:  preTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		replacements:               replacements,
	}, nil
}

func (s *FileStore) CommitToolCompact(chatID string, snapshot ToolCompactSnapshot, line ToolCompactLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return os.ErrPermission
	}
	if chatID != strings.TrimSpace(snapshot.ChatID) {
		return ErrCompactHistoryChanged
	}
	if len(snapshot.replacements) == 0 {
		return ErrNoCompactableHistory
	}
	compactID := strings.TrimSpace(line.CompactID)
	if compactID == "" {
		return ErrNoCompactableHistory
	}
	if line.Type == "" {
		line.Type = ToolCompactLineType
	}
	if line.ChatID == "" {
		line.ChatID = chatID
	}
	if line.Level == "" {
		line.Level = "l1_tools"
	}

	path := s.chatJSONLPath(chatID)
	records, data, err := readJSONLineRecords(path)
	if err != nil {
		return err
	}
	if jsonlContentHash(data) != snapshot.FileHash {
		return ErrCompactHistoryChanged
	}

	replacementsByLine := map[int][]toolCompactReplacement{}
	for _, replacement := range snapshot.replacements {
		replacementsByLine[replacement.LineIndex] = append(replacementsByLine[replacement.LineIndex], replacement)
	}

	backupDir := filepath.Join(s.ChatDir(chatID), ".compact-backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, compactID+".jsonl"), data, 0o644); err != nil {
		return err
	}

	lineBytes, err := validateJSONLLinePayload(line, "chat.jsonl.toolCompact.write")
	if err != nil {
		return err
	}

	var out bytes.Buffer
	for i, record := range records {
		raw := record.Raw
		if replacements := replacementsByLine[i]; len(replacements) > 0 {
			updated, err := applyToolCompactReplacements(record.Value, replacements)
			if err != nil {
				return err
			}
			raw = updated
		}
		out.Write(bytes.TrimSpace(raw))
		out.WriteByte('\n')
	}
	out.Write(lineBytes)
	out.WriteByte('\n')

	tmpPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+compactID+".tmp")
	if err := os.WriteFile(tmpPath, out.Bytes(), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func collectToolCompactCandidates(records []jsonLineRecord) []toolCompactCandidate {
	toolNameByID := map[string]string{}
	candidates := []toolCompactCandidate{}
	for lineIndex, record := range records {
		if lineIsCompacted(record.Value) {
			continue
		}
		lineType := strings.TrimSpace(stringFromAny(record.Value["_type"]))
		if lineType != StepLineTypeReact && lineType != StepLineTypeReactTool {
			continue
		}
		messages, _ := record.Value["messages"].([]any)
		for messageIndex, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			if message == nil {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(stringFromAny(message["role"])))
			switch role {
			case "assistant":
				collectAssistantToolNames(message, toolNameByID)
			case "tool":
				toolID := compactToolResultID(message)
				if toolID == "" {
					continue
				}
				toolName := strings.TrimSpace(toolNameByID[toolID])
				if toolName == "" {
					toolName = strings.TrimSpace(stringFromAny(message["name"]))
				}
				if !toolCompactable(toolName) {
					continue
				}
				content := strings.TrimSpace(anyCompactText(message["content"]))
				candidates = append(candidates, toolCompactCandidate{
					LineIndex:      lineIndex,
					MessageIndex:   messageIndex,
					ToolID:         toolID,
					ToolName:       toolName,
					Content:        content,
					AlreadyCleared: content == ToolCompactClearedMessage,
				})
			}
		}
	}
	return candidates
}

func collectAssistantToolNames(message map[string]any, out map[string]string) {
	rawCalls, _ := message["tool_calls"].([]any)
	for _, rawCall := range rawCalls {
		call, _ := rawCall.(map[string]any)
		if call == nil {
			continue
		}
		id := strings.TrimSpace(stringFromAny(call["id"]))
		if id == "" {
			continue
		}
		function, _ := call["function"].(map[string]any)
		name := strings.TrimSpace(stringFromAny(function["name"]))
		if name == "" {
			name = strings.TrimSpace(stringFromAny(call["name"]))
		}
		if name != "" {
			out[id] = name
		}
	}
}

func compactToolResultID(message map[string]any) string {
	for _, key := range []string{"tool_call_id", "_toolId", "toolId", "actionId", "_actionId"} {
		if id := strings.TrimSpace(stringFromAny(message[key])); id != "" {
			return id
		}
	}
	return ""
}

func toolCompactable(toolName string) bool {
	_, ok := defaultToolCompactableTools[strings.ToLower(strings.TrimSpace(toolName))]
	return ok
}

func applyToolCompactReplacements(line map[string]any, replacements []toolCompactReplacement) ([]byte, error) {
	updated := cloneJSONLineMap(line)
	rawMessages, _ := line["messages"].([]any)
	messages := append([]any(nil), rawMessages...)
	for _, replacement := range replacements {
		if replacement.MessageIndex < 0 || replacement.MessageIndex >= len(messages) {
			continue
		}
		message, _ := messages[replacement.MessageIndex].(map[string]any)
		if message == nil {
			continue
		}
		cloned := cloneJSONLineMap(message)
		cloned["content"] = []map[string]any{{
			"type": "text",
			"text": ToolCompactClearedMessage,
		}}
		messages[replacement.MessageIndex] = cloned
	}
	updated["messages"] = messages
	return json.Marshal(updated)
}
