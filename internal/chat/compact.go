package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultCompactKeptRunCount = 2
)

var ErrNoCompactableHistory = errors.New("no compactable history")
var ErrCompactHistoryChanged = errors.New("compact history changed")
var ErrCompactSummaryInputTooLarge = errors.New("compact summary input too large")

type CompactSnapshot struct {
	LogicalSnapshot            bool
	ChatID                     string
	FileHash                   string
	InsertAfterIndex           int
	CoveredLineCount           int
	ProjectedMessageCount      int
	PreCompactEstimatedTokens  int
	PostCompactEstimatedTokens int
	CompressionRatio           float64
	Prompt                     string
	CoveredMessages            []map[string]any
	TailMessages               []map[string]any
}

type jsonLineRecord struct {
	Raw   []byte
	Value map[string]any
}

func (s *FileStore) BuildCompactSnapshot(chatID string, keptRunCount int) (CompactSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return CompactSnapshot{}, os.ErrPermission
	}
	sum, err := s.loadSummary(chatID)
	if err != nil {
		return CompactSnapshot{}, err
	}
	if sum == nil {
		return CompactSnapshot{}, ErrChatNotFound
	}
	if keptRunCount < 0 {
		keptRunCount = DefaultCompactKeptRunCount
	}

	records, data, err := readJSONLineRecords(s.chatJSONLPath(chatID))
	if err != nil {
		return CompactSnapshot{}, err
	}
	if len(records) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	eligibleRuns, err := s.legacyRepairableRunIDs(chatID)
	if err != nil {
		return CompactSnapshot{}, err
	}

	terminalRuns, err := s.terminalCompactRunIDs(chatID)
	if err != nil {
		return CompactSnapshot{}, err
	}
	for _, record := range records {
		if !lineIsCompacted(record.Value) && len(anyMessageSlice(record.Value["messages"])) > 0 &&
			(record.Value["_type"] == RunCompactCheckpointLineType || record.Value["_type"] == CompactCheckpointLineType) {
			return logicalCompactSnapshot(chatID, records, data, keptRunCount, terminalRuns)
		}
	}
	runOrder, firstRunIndex := activeRootRunOrder(records, terminalRuns)
	if len(runOrder) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	// keptRunCount is a maximum tail, not an eligibility threshold. A single
	// completed root run can itself exhaust the context window, so every
	// snapshot must cover at least one completed run.
	keepCount := keptRunCount
	if keepCount >= len(runOrder) {
		keepCount = len(runOrder) - 1
	}
	boundaryIndex := len(records)
	if keepCount > 0 {
		keepStartRunID := runOrder[len(runOrder)-keepCount]
		boundaryIndex = firstRunIndex[keepStartRunID]
	}
	if boundaryIndex <= 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}

	coveredLines := make([]map[string]any, 0, boundaryIndex)
	coveredLineCount := 0
	for i := 0; i < boundaryIndex; i++ {
		line := records[i].Value
		if lineIsCompacted(line) {
			continue
		}
		coveredLineCount++
		coveredLines = append(coveredLines, line)
	}
	if coveredLineCount == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}

	allLines, err := filterLegacyIncompleteModelTurnsWithRuns(recordValues(records), eligibleRuns)
	if err != nil {
		return CompactSnapshot{}, err
	}
	coveredLines, err = filterLegacyIncompleteModelTurnsWithRuns(coveredLines, eligibleRuns)
	if err != nil {
		return CompactSnapshot{}, err
	}
	tailLines, err := filterLegacyIncompleteModelTurnsWithRuns(recordValues(records[boundaryIndex:]), eligibleRuns)
	if err != nil {
		return CompactSnapshot{}, err
	}
	allMessages := rawMessagesFromJSONLLines(allLines)
	coveredMessages := rawMessagesFromJSONLLines(coveredLines)
	tailMessages := rawMessagesFromJSONLLines(tailLines)
	preTokens := EstimateRawMessageTokens(allMessages)
	postTokens := EstimateRawMessageTokens(tailMessages)
	ratio := 0.0
	if preTokens > 0 {
		ratio = float64(postTokens) / float64(preTokens)
	}

	return CompactSnapshot{
		ChatID:                     chatID,
		FileHash:                   jsonlContentHash(data),
		InsertAfterIndex:           boundaryIndex - 1,
		CoveredLineCount:           coveredLineCount,
		ProjectedMessageCount:      len(coveredMessages),
		PreCompactEstimatedTokens:  preTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		Prompt:                     buildCompactPrompt(coveredMessages),
		CoveredMessages:            coveredMessages,
		TailMessages:               tailMessages,
	}, nil
}

func (s *FileStore) terminalCompactRunIDs(chatID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_ FROM RUNS WHERE CHAT_ID_=? AND COMPLETED_AT_>0`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := map[string]bool{}
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		if runID = strings.TrimSpace(runID); runID != "" {
			runs[runID] = true
		}
	}
	return runs, rows.Err()
}

func (s *FileStore) CommitCompactCheckpoint(chatID string, snapshot CompactSnapshot, checkpoint CompactCheckpointLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return os.ErrPermission
	}
	if chatID != strings.TrimSpace(snapshot.ChatID) {
		return fmt.Errorf("compact snapshot chatId mismatch")
	}
	compactID := strings.TrimSpace(checkpoint.CompactID)
	if compactID == "" {
		return fmt.Errorf("compactId is required")
	}
	if checkpoint.Type == "" {
		checkpoint.Type = CompactCheckpointLineType
	}
	if checkpoint.ChatID == "" {
		checkpoint.ChatID = chatID
	}
	if checkpoint.CompactionUsage == nil {
		checkpoint.CompactionUsage = map[string]any{}
	}

	path := s.chatJSONLPath(chatID)
	records, data, err := readJSONLineRecords(path)
	if err != nil {
		return err
	}
	if jsonlContentHash(data) != snapshot.FileHash {
		return ErrCompactHistoryChanged
	}
	if snapshot.InsertAfterIndex < 0 || snapshot.InsertAfterIndex >= len(records) {
		return ErrNoCompactableHistory
	}

	checkpoint.Version = 2
	checkpoint.CoveredThroughLine = snapshot.InsertAfterIndex + 1
	checkpoint.PreviousCompactID = previousEffectiveCompactID(records)
	if snapshot.LogicalSnapshot {
		checkpoint.Messages = append([]map[string]any{{"role": "user", "content": CompactCheckpointSummaryMessage(checkpoint.Summary)}}, snapshot.TailMessages...)
	}
	checkpointBytes, err := validateJSONLLinePayload(checkpoint, "chat.jsonl.compact.write")
	if err != nil {
		return err
	}

	var out bytes.Buffer
	for i, record := range records {
		lineBytes := record.Raw
		if i <= snapshot.InsertAfterIndex && !lineIsCompacted(record.Value) {
			marked := cloneJSONLineMap(record.Value)
			marked["_compact"] = compactID
			lineBytes, err = json.Marshal(marked)
			if err != nil {
				return err
			}
		}
		out.Write(bytes.TrimSpace(lineBytes))
		out.WriteByte('\n')
		if i == snapshot.InsertAfterIndex {
			out.Write(checkpointBytes)
			out.WriteByte('\n')
		}
	}

	backupDir := filepath.Join(s.ChatDir(chatID), ".compact-backups")
	return replaceChatJSONLWithValidatedCompact(path, backupDir, compactID, data, out.Bytes())
}

func replaceChatJSONLWithValidatedCompact(path string, backupDir string, compactID string, originalData []byte, compactedData []byte) error {
	records, err := decodeJSONLRecords(compactedData, "chat.jsonl.compact.write", true)
	if err != nil {
		return err
	}
	if err := validatePersistedTimeContract(recordValues(records), "chat.jsonl.compact.write"); err != nil {
		return err
	}

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, compactID+".jsonl"), originalData, 0o644); err != nil {
		return err
	}

	tmpPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+compactID+".tmp")
	if err := os.WriteFile(tmpPath, compactedData, 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func readJSONLineRecords(path string) ([]jsonLineRecord, []byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []jsonLineRecord{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	records, err := decodeJSONLRecords(data, "chat.jsonl", true)
	if err != nil {
		return nil, nil, err
	}
	if err := validatePersistedTimeContract(recordValues(records), "chat.jsonl"); err != nil {
		return nil, nil, err
	}
	return records, data, nil
}

func jsonlContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func activeRootRunOrder(records []jsonLineRecord, eligibleRuns map[string]bool) ([]string, map[string]int) {
	order := []string{}
	seen := map[string]bool{}
	firstIndex := map[string]int{}
	for i, record := range records {
		runID := compactRootRunID(record.Value)
		if len(eligibleRuns) > 0 && !eligibleRuns[runID] {
			continue
		}
		if runID == "" || seen[runID] {
			continue
		}
		seen[runID] = true
		firstIndex[runID] = i
		order = append(order, runID)
	}
	return order, firstIndex
}

func compactRootRunID(line map[string]any) string {
	if lineIsCompacted(line) {
		return ""
	}
	lineType := strings.TrimSpace(stringFromAny(line["_type"]))
	if lineType == CompactCheckpointLineType || lineType == RunCompactCheckpointLineType {
		return ""
	}
	runID := strings.TrimSpace(stringFromAny(line["runId"]))
	if runID == "" {
		return ""
	}
	if strings.TrimSpace(stringFromAny(line["taskId"])) != "" {
		return ""
	}
	if strings.TrimSpace(stringFromAny(line["taskSubAgentKey"])) != "" {
		return ""
	}
	if strings.TrimSpace(stringFromAny(line["subAgentKey"])) != "" {
		return ""
	}
	return runID
}

func lineIsCompacted(line map[string]any) bool {
	if line == nil {
		return false
	}
	_, ok := line["_compact"]
	return ok
}

func hasActiveCompactCheckpoint(lines []map[string]any) bool {
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		lineType := strings.TrimSpace(stringFromAny(line["_type"]))
		if lineType == CompactCheckpointLineType || lineType == RunCompactCheckpointLineType {
			return true
		}
	}
	return false
}

func activeCompactCheckpointSummary(line map[string]any) (string, bool) {
	if lineIsCompacted(line) {
		return "", false
	}
	if strings.TrimSpace(stringFromAny(line["_type"])) != CompactCheckpointLineType {
		return "", false
	}
	summary := strings.TrimSpace(stringFromAny(line["summary"]))
	return summary, summary != ""
}

func compactCheckpointSummaryMessage(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "以下是此前对话的上下文压缩摘要。它替代所有已标记 _compact 的历史。\n\n" + summary
}

func recordValues(records []jsonLineRecord) []map[string]any {
	values := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record.Value != nil {
			values = append(values, record.Value)
		}
	}
	return values
}

func cloneJSONLineMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func buildCompactPrompt(messages []map[string]any) string {
	mediaProjected, _ := projectCompactMediaMessages(messages, true)
	rendered := renderMessagesForCompact(normalizeCompactSummaryMessages(mediaProjected), 0)
	if strings.TrimSpace(rendered) == "" {
		return ""
	}
	return strings.TrimSpace(`你正在为一个长期对话生成上下文压缩摘要。

请只基于下面提供的历史消息，总结后续继续对话必须知道的信息。要求：
- 按时间顺序保留用户目标及其变化、已确认偏好、重要约束和关键决策。
- 明确区分已完成工作、验证结果、失败与解决方式、尚未完成事项和下一步。
- 保留重要文件路径、接口、参数名、错误信息、产物、URL、SHA、ID 与测试锚点。
- 工具记录已经做过确定性降噪；从中提取重要事实，不要复述工具协议包装。
- 不要编造未出现的信息。
- 输出中文，使用简洁的分段或要点。
- 不要解释你在做压缩，也不要包含寒暄。

历史消息如下：

` + rendered)
}

// BuildCompactPromptWithinBudget always renders the complete normalized
// history. If one summary model call cannot contain it, callers must fail
// explicitly rather than dropping the middle or recursively summarizing.
func BuildCompactPromptWithinBudget(messages []map[string]any, maxInputTokens int) (string, error) {
	prompt := buildCompactPrompt(messages)
	if strings.TrimSpace(prompt) == "" {
		return "", nil
	}
	if maxInputTokens > 0 && EstimateTextTokens(prompt) > maxInputTokens {
		return "", ErrCompactSummaryInputTooLarge
	}
	return prompt, nil
}

func normalizeCompactSummaryMessages(messages []map[string]any) []map[string]any {
	projected, _, _ := CompactToolMessages(messages, 0, 0, -1, -1)
	return projected
}

func renderMessagesForCompact(messages []map[string]any, maxChars int) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	for i, msg := range messages {
		encoded, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("%04d ", i+1))
		b.Write(encoded)
		if maxChars > 0 && b.Len() > maxChars {
			return truncateMiddle(b.String(), maxChars)
		}
	}
	return b.String()
}

func CompactCheckpointSummaryMessage(summary string) string {
	return compactCheckpointSummaryMessage(summary)
}

func compactMessageSnippet(msg map[string]any, maxChars int) string {
	text := strings.TrimSpace(anyCompactText(msg["content"]))
	if text == "" {
		text = strings.TrimSpace(anyCompactText(msg["reasoning_content"]))
	}
	if text == "" {
		encoded, err := json.Marshal(msg)
		if err == nil {
			text = string(encoded)
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	return truncateString(text, maxChars)
}

func anyCompactText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch typed := item.(type) {
			case string:
				parts = append(parts, typed)
			case map[string]any:
				if text := strings.TrimSpace(stringFromAny(typed["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func EstimateRawMessageTokens(messages []map[string]any) int {
	if len(messages) == 0 {
		return 0
	}
	projected, mediaTokens := projectCompactMediaMessages(messages, false)
	for _, message := range projected {
		for _, key := range []string{"runId", "agentKey", "taskSubAgentKey", "msgId", "ts", "_compactPinned"} {
			delete(message, key)
		}
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		total := mediaTokens
		for _, msg := range projected {
			total += EstimateTextTokens(compactMessageSnippet(msg, 2000))
		}
		return total
	}
	return EstimateTextTokens(string(encoded)) + mediaTokens
}

func EstimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if tokens <= 0 {
		return 1
	}
	return tokens
}

func EstimateCompactPostTokens(summary string, tailMessages []map[string]any) int {
	messages := make([]map[string]any, 0, len(tailMessages)+1)
	if compacted := compactCheckpointSummaryMessage(summary); compacted != "" {
		messages = append(messages, map[string]any{"role": "user", "content": compacted})
	}
	messages = append(messages, tailMessages...)
	return EstimateRawMessageTokens(messages)
}

func truncateMiddle(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	half := maxChars / 2
	return string(runes[:half]) + "\n...[truncated]...\n" + string(runes[len(runes)-(maxChars-half):])
}

func truncateString(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "..."
}
