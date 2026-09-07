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

	"agent-platform/internal/compaction"
)

const (
	DefaultToolCompactKeepRecent = compaction.KeepRecentTools
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
	var result map[string]any
	_ = json.Unmarshal([]byte(text), &result)
	if value, ok := content.(map[string]any); ok {
		result = value
	}
	if result["success"] == false || result["isError"] == true ||
		(result["error"] != nil && result["error"] != "") ||
		result["status"] == "failed" || result["status"] == "error" ||
		(result["exitCode"] != nil && fmt.Sprint(result["exitCode"]) != "0") {
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
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := typed[key]
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

	protected := protectedToolBatches(candidates, keepRecent)
	preTokens := EstimateRawMessageTokens(rawMessagesFromJSONLLines(recordValues(records)))
	replacements := make([]toolCompactReplacement, 0, len(candidates))
	tokensFreed := 0
	for _, candidate := range candidates {
		if targetTokens > 0 && preTokens-tokensFreed <= targetTokens {
			break
		}
		if protected[toolBatchKey(candidate)] || !ToolCompactable(candidate.ToolName) {
			continue
		}
		replacement, freed, ok := toolReplacement(candidate)
		if !ok {
			continue
		}
		replacements = append(replacements, replacement)
		tokensFreed += freed
	}

	projectedRecords := append([]jsonLineRecord(nil), records...)
	for _, replacement := range replacements {
		indices := []int{replacement.LineIndex}
		if replacement.AssistantLineIndex != replacement.LineIndex {
			indices = append(indices, replacement.AssistantLineIndex)
		}
		for _, index := range indices {
			raw, err := applyToolCompactReplacements(projectedRecords[index].Value, index, []toolCompactReplacement{replacement})
			if err != nil {
				return ToolCompactSnapshot{}, err
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				return ToolCompactSnapshot{}, err
			}
			projectedRecords[index] = jsonLineRecord{Raw: raw, Value: value}
		}
	}
	postTokens := EstimateRawMessageTokens(rawMessagesFromJSONLLines(recordValues(projectedRecords)))
	if postTokens >= preTokens {
		replacements = nil
		postTokens = preTokens
	}
	tokensFreed = max(0, preTokens-postTokens)
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
	line.Version = 2
	line.CoveredThroughLine = len(records)
	line.PreviousCompactID = previousEffectiveCompactID(records)

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

// collectToolCompactCandidates reads the effective checkpoint and subsequent
// steps only. Calls are matched in order within their run/actor, never globally.
func collectToolCompactCandidates(records []jsonLineRecord) []toolCompactCandidate {
	type pendingCall struct {
		candidate toolCompactCandidate
		matched   bool
		pinned    bool
	}
	type batch struct{ calls []*pendingCall }
	var batches []*batch
	pending := map[string]*pendingCall{}
	for lineIndex, record := range records {
		line := record.Value
		if lineIsCompacted(line) {
			continue
		}
		lineType := stringFromAny(line["_type"])
		if lineType == RunCompactCheckpointLineType || (lineType == CompactCheckpointLineType && len(anyMessageSlice(line["messages"])) > 0) {
			batches = nil
			pending = map[string]*pendingCall{}
		} else if lineType != StepLineTypeReact && lineType != StepLineTypeReactTool {
			continue
		}
		if stringFromAny(line["taskSubAgentKey"]) != "" {
			continue
		}
		for messageIndex, message := range anyMessageSlice(line["messages"]) {
			runID := stringFromAny(message["runId"])
			if runID == "" {
				runID = stringFromAny(line["runId"])
			}
			actor := stringFromAny(message["taskSubAgentKey"]) + ":" + stringFromAny(message["agentKey"])
			scope := runID + "\x00" + actor + "\x00"
			switch stringFromAny(message["role"]) {
			case "assistant":
				calls := anyMessageSlice(message["tool_calls"])
				if len(calls) == 0 {
					continue
				}
				group := &batch{}
				for callIndex, call := range calls {
					id := stringFromAny(call["id"])
					function, _ := call["function"].(map[string]any)
					item := &pendingCall{candidate: toolCompactCandidate{
						AssistantLineIndex: lineIndex, AssistantMessageIndex: messageIndex,
						AssistantCallIndex: callIndex, ToolID: id, ToolName: stringFromAny(function["name"]),
						Arguments: stringFromAny(function["arguments"]),
					}, pinned: message["_compactPinned"] == true}
					group.calls = append(group.calls, item)
					if id != "" {
						pending[scope+id] = item
					}
				}
				batches = append(batches, group)
			case "tool":
				item := pending[scope+compactToolResultID(message)]
				if item == nil || item.matched {
					continue
				}
				item.matched = true
				item.pinned = item.pinned || message["_compactPinned"] == true
				item.candidate.LineIndex = lineIndex
				item.candidate.MessageIndex = messageIndex
				item.candidate.Content = strings.TrimSpace(anyCompactText(message["content"]))
				content := item.candidate.Content
				item.candidate.AlreadyCleared = content == ToolCompactClearedMessage || strings.HasPrefix(content, "[Compacted tool interaction]")
			}
		}
	}
	var out []toolCompactCandidate
	for _, group := range batches {
		complete := true
		for _, item := range group.calls {
			if !item.matched || item.pinned {
				complete = false
			}
		}
		if complete {
			for _, item := range group.calls {
				out = append(out, item.candidate)
			}
		}
	}
	return out
}

func toolBatchKey(candidate toolCompactCandidate) [2]int {
	return [2]int{candidate.AssistantLineIndex, candidate.AssistantMessageIndex}
}

func protectedToolBatches(candidates []toolCompactCandidate, keepRecent int) map[[2]int]bool {
	protected := map[[2]int]bool{}
	for i := max(0, len(candidates)-keepRecent); i < len(candidates); i++ {
		protected[toolBatchKey(candidates[i])] = true
	}
	return protected
}

func toolReplacement(candidate toolCompactCandidate) (toolCompactReplacement, int, bool) {
	if candidate.AlreadyCleared || strings.TrimSpace(candidate.Content) == "" {
		return toolCompactReplacement{}, 0, false
	}
	digest := ToolCompactDigest(candidate.ToolName, candidate.ToolID, candidate.Content)
	arguments := CompactToolArguments(candidate.Arguments)
	freed := EstimateTextTokens(candidate.Content) + EstimateTextTokens(candidate.Arguments) -
		EstimateTextTokens(digest) - EstimateTextTokens(arguments)
	if freed <= 0 {
		return toolCompactReplacement{}, 0, false
	}
	return toolCompactReplacement{
		LineIndex: candidate.LineIndex, MessageIndex: candidate.MessageIndex,
		Content:            []map[string]any{{"type": "text", "text": digest}},
		AssistantLineIndex: candidate.AssistantLineIndex, AssistantMessageIndex: candidate.AssistantMessageIndex,
		AssistantCallIndex: candidate.AssistantCallIndex, Arguments: arguments,
	}, freed, true
}

// CompactToolMessages is the shared, non-mutating L1 projection used by active
// runs and summary input normalization. A zero keepRecent is allowed for L2's
// in-memory projection; public L1 always passes KeepRecentTools.
func CompactToolMessages(messages []map[string]any, keepRecent, targetTokens, pinnedStart, pinnedEnd int) ([]map[string]any, int, int) {
	cloned := make([]map[string]any, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessageMap(message)
		if i >= pinnedStart && i < pinnedEnd {
			cloned[i]["_compactPinned"] = true
		}
	}
	items := make([]any, len(cloned))
	for i := range cloned {
		items[i] = cloned[i]
	}
	line := map[string]any{"_type": StepLineTypeReact, "messages": items}
	records := []jsonLineRecord{{Value: line}}
	candidates := collectToolCompactCandidates(records)
	protected := protectedToolBatches(candidates, keepRecent)
	out := line
	cleared := 0
	for _, candidate := range candidates {
		if targetTokens > 0 && EstimateRawMessageTokens(anyMessageSlice(out["messages"])) <= targetTokens {
			break
		}
		if protected[toolBatchKey(candidate)] || !ToolCompactable(candidate.ToolName) {
			continue
		}
		replacement, _, ok := toolReplacement(candidate)
		if !ok {
			continue
		}
		raw, err := applyToolCompactReplacements(out, 0, []toolCompactReplacement{replacement})
		if err != nil {
			continue
		}
		var next map[string]any
		if json.Unmarshal(raw, &next) != nil {
			continue
		}
		out = next
		cleared++
	}
	result := anyMessageSlice(out["messages"])
	for _, message := range result {
		delete(message, "_compactPinned")
	}
	return result, cleared, len(candidates) - cleared
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
