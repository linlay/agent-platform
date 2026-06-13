package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultCompactKeptRunCount = 2
	compactPromptMaxChars      = 60000
	compactFallbackMaxItems    = 24
)

var ErrNoCompactableHistory = errors.New("no compactable history")
var ErrCompactHistoryChanged = errors.New("compact history changed")

type CompactSnapshot struct {
	ChatID                     string
	FileHash                   string
	InsertAfterIndex           int
	CoveredLineCount           int
	ProjectedMessageCount      int
	PreCompactEstimatedTokens  int
	PostCompactEstimatedTokens int
	CompressionRatio           float64
	Prompt                     string
	FallbackSummary            string
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
	if keptRunCount <= 0 {
		keptRunCount = DefaultCompactKeptRunCount
	}

	records, data, err := readJSONLineRecords(s.chatJSONLPath(chatID))
	if err != nil {
		return CompactSnapshot{}, err
	}
	if len(records) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}

	runOrder, firstRunIndex := activeRootRunOrder(records)
	if len(runOrder) <= keptRunCount {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	keepStartRunID := runOrder[len(runOrder)-keptRunCount]
	boundaryIndex := firstRunIndex[keepStartRunID]
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

	allMessages := rawMessagesFromJSONLLines(recordValues(records))
	coveredMessages := rawMessagesFromJSONLLines(coveredLines)
	tailMessages := rawMessagesFromJSONLLines(recordValues(records[boundaryIndex:]))
	fallbackSummary := deterministicCompactSummary(coveredMessages)
	preTokens := EstimateRawMessageTokens(allMessages)
	postTokens := EstimateCompactPostTokens(fallbackSummary, tailMessages)
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
		FallbackSummary:            fallbackSummary,
		TailMessages:               tailMessages,
	}, nil
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

	backupDir := filepath.Join(s.ChatDir(chatID), ".compact-backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, compactID+".jsonl"), data, 0o644); err != nil {
		return err
	}

	checkpointBytes, err := json.Marshal(checkpoint)
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

func readJSONLineRecords(path string) ([]jsonLineRecord, []byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []jsonLineRecord{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	records := []jsonLineRecord{}
	for {
		start := decoder.InputOffset()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("parse JSONL: %w", err)
		}
		end := decoder.InputOffset()
		raw := bytes.TrimSpace(data[int(start):int(end)])
		if len(raw) == 0 {
			raw, _ = json.Marshal(payload)
		}
		if payload != nil {
			records = append(records, jsonLineRecord{Raw: raw, Value: payload})
		}
	}
	return records, data, nil
}

func jsonlContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func activeRootRunOrder(records []jsonLineRecord) ([]string, map[string]int) {
	order := []string{}
	seen := map[string]bool{}
	firstIndex := map[string]int{}
	for i, record := range records {
		runID := compactRootRunID(record.Value)
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
	if lineType == CompactCheckpointLineType {
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
		if strings.TrimSpace(stringFromAny(line["_type"])) == CompactCheckpointLineType {
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
	rendered := renderMessagesForCompact(messages, compactPromptMaxChars)
	if strings.TrimSpace(rendered) == "" {
		return ""
	}
	return strings.TrimSpace(`你正在为一个长期对话生成上下文压缩摘要。

请只基于下面提供的历史消息，总结后续继续对话必须知道的信息。要求：
- 保留用户目标、已确认的偏好、重要约束、关键决策、尚未完成的事项。
- 保留重要文件路径、接口、参数名、错误信息、结论和下一步。
- 不要编造未出现的信息。
- 输出中文，使用简洁的分段或要点。
- 不要解释你在做压缩，也不要包含寒暄。

历史消息如下：

` + rendered)
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

func deterministicCompactSummary(messages []map[string]any) string {
	if len(messages) == 0 {
		return ""
	}
	runIDs := map[string]bool{}
	roleCounts := map[string]int{}
	for _, msg := range messages {
		if runID := strings.TrimSpace(stringFromAny(msg["runId"])); runID != "" {
			runIDs[runID] = true
		}
		role := strings.TrimSpace(stringFromAny(msg["role"]))
		if role == "" {
			role = "unknown"
		}
		roleCounts[role]++
	}
	roles := make([]string, 0, len(roleCounts))
	for role, count := range roleCounts {
		roles = append(roles, fmt.Sprintf("%s:%d", role, count))
	}
	sort.Strings(roles)

	var b strings.Builder
	b.WriteString("上下文压缩摘要（deterministic fallback）：\n")
	b.WriteString(fmt.Sprintf("- 覆盖消息数：%d\n", len(messages)))
	b.WriteString(fmt.Sprintf("- 覆盖 root run 数：%d\n", len(runIDs)))
	if len(roles) > 0 {
		b.WriteString("- 角色分布：" + strings.Join(roles, ", ") + "\n")
	}
	b.WriteString("- 关键内容摘录：\n")
	for _, msg := range compactSummarySampleMessages(messages, compactFallbackMaxItems) {
		role := strings.TrimSpace(stringFromAny(msg["role"]))
		if role == "" {
			role = "unknown"
		}
		text := compactMessageSnippet(msg, 360)
		if text == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("  - [%s] %s\n", role, text))
	}
	return strings.TrimSpace(b.String())
}

func compactSummarySampleMessages(messages []map[string]any, limit int) []map[string]any {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	headCount := limit / 2
	tailCount := limit - headCount
	sampled := make([]map[string]any, 0, limit)
	sampled = append(sampled, messages[:headCount]...)
	sampled = append(sampled, messages[len(messages)-tailCount:]...)
	return sampled
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
	encoded, err := json.Marshal(messages)
	if err != nil {
		total := 0
		for _, msg := range messages {
			total += EstimateTextTokens(compactMessageSnippet(msg, 2000))
		}
		return total
	}
	return EstimateTextTokens(string(encoded))
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
