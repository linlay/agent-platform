package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultToolCompactKeepRecent = 5
	ToolCompactClearedMessage    = "[Old tool result content cleared]" // legacy replay marker
	toolCompactMaxExcerptRunes   = 72
	toolCompactMaxScalarRunes    = 180
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
	LineIndex             int
	MessageIndex          int
	Content               any
	AssistantLineIndex    int
	AssistantMessageIndex int
	AssistantCallIndex    int
	Arguments             string
}

type toolCompactCandidate struct {
	LineIndex             int
	MessageIndex          int
	ToolID                string
	ToolName              string
	Content               string
	AlreadyCleared        bool
	AssistantLineIndex    int
	AssistantMessageIndex int
	AssistantCallIndex    int
	Arguments             string
}

type toolCompactCallLocation struct {
	ToolName     string
	LineIndex    int
	MessageIndex int
	CallIndex    int
	Arguments    string
	SiblingIDs   []string
}

// ToolCompactDigest returns a deterministic, bounded, auditable replacement
// for a completed tool result. It deliberately keeps protocol identity and
// useful scalar metadata while removing the potentially unbounded body.
func ToolCompactDigest(toolName, toolID string, content any) string {
	text := strings.TrimSpace(anyCompactText(content))
	encoded, _ := json.Marshal(content)
	if len(encoded) == 0 {
		encoded = []byte(text)
	}
	sum := sha256.Sum256(encoded)
	status := "success"
	lower := strings.ToLower(text)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "exception") {
		status = "error"
	}
	metadata := compactToolMetadata(content)
	var b strings.Builder
	b.WriteString("[Compacted tool interaction]\n")
	b.WriteString("tool: " + strings.TrimSpace(toolName) + "\n")
	b.WriteString("toolCallId: " + strings.TrimSpace(toolID) + "\n")
	b.WriteString("status: " + status + "\n")
	b.WriteString(fmt.Sprintf("originalEstimatedTokens: %d\n", EstimateTextTokens(string(encoded))))
	b.WriteString("contentSha256: sha256:" + hex.EncodeToString(sum[:]) + "\n")
	for _, key := range sortedCompactMetadataKeys(metadata) {
		b.WriteString(key + ": " + metadata[key] + "\n")
	}
	if excerpt := compactToolExcerpt(text, toolCompactMaxExcerptRunes); excerpt != "" {
		b.WriteString("summary: " + excerpt)
	} else {
		b.WriteString("summary: 工具已完成；原始正文已从模型上下文中移除。")
	}
	return strings.TrimSpace(b.String())
}

// CompactToolArguments preserves valid JSON, protocol identity and useful
// scalar fields while replacing large nested/string arguments with hashes.
func CompactToolArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || len([]rune(arguments)) <= toolCompactMaxScalarRunes*2 {
		return arguments
	}
	var value any
	if json.Unmarshal([]byte(arguments), &value) != nil {
		return arguments
	}
	compacted := compactToolArgumentValue(value, 0)
	encoded, err := json.Marshal(compacted)
	if err != nil || len(encoded) >= len(arguments) {
		return arguments
	}
	return string(encoded)
}

func compactToolArgumentValue(value any, depth int) any {
	if depth > 4 {
		encoded, _ := json.Marshal(value)
		sum := sha256.Sum256(encoded)
		return map[string]any{"_compacted": true, "sha256": "sha256:" + hex.EncodeToString(sum[:]), "bytes": len(encoded)}
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = compactToolArgumentValue(item, depth+1)
		}
		return out
	case []any:
		if len(typed) <= 12 {
			out := make([]any, len(typed))
			for i, item := range typed {
				out[i] = compactToolArgumentValue(item, depth+1)
			}
			return out
		}
		encoded, _ := json.Marshal(typed)
		sum := sha256.Sum256(encoded)
		return map[string]any{"_compacted": true, "items": len(typed), "sha256": "sha256:" + hex.EncodeToString(sum[:])}
	case string:
		runes := []rune(typed)
		if len(runes) <= toolCompactMaxScalarRunes {
			return typed
		}
		sum := sha256.Sum256([]byte(typed))
		return map[string]any{
			"_compacted": true,
			"chars":      len(runes),
			"sha256":     "sha256:" + hex.EncodeToString(sum[:]),
			"preview":    compactToolExcerpt(typed, toolCompactMaxScalarRunes),
		}
	default:
		return value
	}
}

func compactToolMetadata(content any) map[string]string {
	metadata := map[string]string{}
	var decoded any = content
	if text, ok := content.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			decoded = parsed
		}
	}
	allowed := map[string]bool{
		"path": true, "filepath": true, "url": true, "artifactid": true,
		"documentid": true, "exitcode": true, "code": true, "size": true,
		"sizebytes": true, "byteswritten": true, "sha256": true, "error": true,
		"message": true, "status": true,
	}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 4 || len(metadata) >= 12 {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
				if allowed[normalized] {
					if scalar := compactScalarText(item); scalar != "" {
						metadata[key] = scalar
					}
				}
				visit(item, depth+1)
			}
		case []any:
			for _, item := range typed {
				visit(item, depth+1)
			}
		}
	}
	visit(decoded, 0)
	return metadata
}

func compactScalarText(value any) string {
	switch typed := value.(type) {
	case string:
		return compactToolExcerpt(typed, toolCompactMaxScalarRunes)
	case float64, float32, int, int64, int32, bool, json.Number:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}

func sortedCompactMetadataKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compactToolExcerpt(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	head := maxRunes * 2 / 3
	tail := maxRunes - head
	return strings.TrimSpace(string(runes[:head])) + " … " + strings.TrimSpace(string(runes[len(runes)-tail:]))
}

func (s *FileStore) BuildToolCompactSnapshot(chatID string, keepRecent int) (ToolCompactSnapshot, error) {
	return s.BuildToolCompactSnapshotToTarget(chatID, keepRecent, 0)
}

// BuildToolCompactSnapshotToTarget normally protects the most recent complete
// tool groups, then progressively releases that protection only while the
// projected history remains above targetTokens. A non-positive target keeps
// the legacy/manual behavior of allowing even a single large group.
func (s *FileStore) BuildToolCompactSnapshotToTarget(chatID string, keepRecent, targetTokens int) (ToolCompactSnapshot, error) {
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

	preferredCount := len(candidates) - keepRecent
	if preferredCount < 0 {
		preferredCount = 0
	}
	preTokens := EstimateRawMessageTokens(rawMessagesFromJSONLLines(recordValues(records)))
	replacements := make([]toolCompactReplacement, 0, len(candidates))
	tokensFreed := 0
	for index, candidate := range candidates {
		if index >= preferredCount {
			if targetTokens > 0 && preTokens-tokensFreed <= targetTokens {
				break
			}
			if targetTokens <= 0 && preferredCount > 0 {
				break
			}
		}
		if candidate.AlreadyCleared || strings.TrimSpace(candidate.Content) == "" {
			continue
		}
		digest := ToolCompactDigest(candidate.ToolName, candidate.ToolID, candidate.Content)
		arguments := CompactToolArguments(candidate.Arguments)
		originalCost := EstimateTextTokens(candidate.Content) + EstimateTextTokens(candidate.Arguments)
		compactCost := EstimateTextTokens(digest) + EstimateTextTokens(arguments)
		if compactCost >= originalCost {
			continue
		}
		replacements = append(replacements, toolCompactReplacement{
			LineIndex:    candidate.LineIndex,
			MessageIndex: candidate.MessageIndex,
			Content: []map[string]any{{
				"type": "text",
				"text": digest,
			}},
			AssistantLineIndex:    candidate.AssistantLineIndex,
			AssistantMessageIndex: candidate.AssistantMessageIndex,
			AssistantCallIndex:    candidate.AssistantCallIndex,
			Arguments:             arguments,
		})
		freed := originalCost - compactCost
		if freed > 0 {
			tokensFreed += freed
		}
	}

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
		ToolsKept:                  len(candidates) - len(replacements),
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
		if replacement.AssistantLineIndex != replacement.LineIndex {
			replacementsByLine[replacement.AssistantLineIndex] = append(replacementsByLine[replacement.AssistantLineIndex], replacement)
		}
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
			updated, err := applyToolCompactReplacements(record.Value, i, replacements)
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
	callByID := map[string]toolCompactCallLocation{}
	resultIDs := map[string]bool{}
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
			if role == "assistant" {
				collectAssistantToolLocations(message, lineIndex, messageIndex, callByID)
			} else if role == "tool" {
				if id := compactToolResultID(message); id != "" {
					resultIDs[id] = true
				}
			}
		}
	}
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
			case "tool":
				toolID := compactToolResultID(message)
				if toolID == "" {
					continue
				}
				call, found := callByID[toolID]
				if !found || !toolCompactCallComplete(call, resultIDs) {
					continue
				}
				toolName := strings.TrimSpace(call.ToolName)
				if toolName == "" {
					toolName = strings.TrimSpace(stringFromAny(message["name"]))
				}
				if !toolCompactable(toolName) {
					continue
				}
				content := strings.TrimSpace(anyCompactText(message["content"]))
				candidates = append(candidates, toolCompactCandidate{
					LineIndex:             lineIndex,
					MessageIndex:          messageIndex,
					ToolID:                toolID,
					ToolName:              toolName,
					Content:               content,
					AlreadyCleared:        content == ToolCompactClearedMessage || strings.HasPrefix(content, "[Compacted tool interaction]"),
					AssistantLineIndex:    call.LineIndex,
					AssistantMessageIndex: call.MessageIndex,
					AssistantCallIndex:    call.CallIndex,
					Arguments:             call.Arguments,
				})
			}
		}
	}
	return candidates
}

func collectAssistantToolLocations(message map[string]any, lineIndex, messageIndex int, out map[string]toolCompactCallLocation) {
	rawCalls, _ := message["tool_calls"].([]any)
	siblingIDs := make([]string, 0, len(rawCalls))
	for _, rawCall := range rawCalls {
		call, _ := rawCall.(map[string]any)
		if id := strings.TrimSpace(stringFromAny(call["id"])); id != "" {
			siblingIDs = append(siblingIDs, id)
		}
	}
	for callIndex, rawCall := range rawCalls {
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
		out[id] = toolCompactCallLocation{
			ToolName: name, LineIndex: lineIndex, MessageIndex: messageIndex,
			CallIndex: callIndex, Arguments: strings.TrimSpace(stringFromAny(function["arguments"])),
			SiblingIDs: append([]string(nil), siblingIDs...),
		}
	}
}

func toolCompactCallComplete(call toolCompactCallLocation, resultIDs map[string]bool) bool {
	if len(call.SiblingIDs) == 0 {
		return false
	}
	for _, id := range call.SiblingIDs {
		if !resultIDs[id] {
			return false
		}
	}
	return true
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
	for _, key := range []string{"tool_call_id", "_toolId", "toolId"} {
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

func ToolCompactable(toolName string) bool {
	return toolCompactable(toolName)
}

func applyToolCompactReplacements(line map[string]any, lineIndex int, replacements []toolCompactReplacement) ([]byte, error) {
	updated := cloneJSONLineMap(line)
	rawMessages, _ := line["messages"].([]any)
	messages := append([]any(nil), rawMessages...)
	for _, replacement := range replacements {
		if replacement.LineIndex != lineIndex {
			continue
		}
		if replacement.MessageIndex < 0 || replacement.MessageIndex >= len(messages) {
			continue
		}
		message, _ := messages[replacement.MessageIndex].(map[string]any)
		if message == nil {
			continue
		}
		cloned := cloneJSONLineMap(message)
		cloned["content"] = replacement.Content
		messages[replacement.MessageIndex] = cloned
	}
	for _, replacement := range replacements {
		if replacement.AssistantLineIndex != lineIndex {
			continue
		}
		if replacement.AssistantMessageIndex < 0 || replacement.AssistantMessageIndex >= len(messages) || replacement.Arguments == "" {
			continue
		}
		message, _ := messages[replacement.AssistantMessageIndex].(map[string]any)
		if message == nil {
			continue
		}
		cloned := cloneJSONLineMap(message)
		rawCalls, _ := message["tool_calls"].([]any)
		calls := append([]any(nil), rawCalls...)
		if replacement.AssistantCallIndex < 0 || replacement.AssistantCallIndex >= len(calls) {
			continue
		}
		call, _ := calls[replacement.AssistantCallIndex].(map[string]any)
		if call == nil {
			continue
		}
		clonedCall := cloneJSONLineMap(call)
		function, _ := call["function"].(map[string]any)
		clonedFunction := cloneJSONLineMap(function)
		clonedFunction["arguments"] = replacement.Arguments
		clonedCall["function"] = clonedFunction
		calls[replacement.AssistantCallIndex] = clonedCall
		cloned["tool_calls"] = calls
		messages[replacement.AssistantMessageIndex] = cloned
	}
	updated["messages"] = messages
	return json.Marshal(updated)
}
