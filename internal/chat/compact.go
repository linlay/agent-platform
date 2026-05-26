package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	DefaultCompactKeptRunCount     = 2
	DefaultCompactToolResultChars  = 4096
	DefaultCompactRecentToolChars  = 8192
	defaultCompactPromptMaxChars   = 60000
	defaultCompactSnippetHeadChars = 1200
	defaultCompactSnippetTailChars = 1200
)

var ErrNoCompactableHistory = errors.New("no compactable history")

type CompactSnapshot struct {
	BoundarySeq         int
	BoundaryRunID       string
	Generation          int
	CompactedRunCount   int
	OriginalMessages    int
	ProjectedMessages   int
	PreCompactTokens    int
	PostCompactTokens   int
	CompressionRatio    float64
	SummaryTargetTokens int
	Prompt              string
	FallbackSummary     string
	ToolDigests         []ToolDigest
	DigestedRunIDs      []string
	CacheMetrics        map[string]any
	TailMessages        []map[string]any
}

func (s *FileStore) BuildCompactSnapshot(chatID string, keptRunCount int) (CompactSnapshot, error) {
	if keptRunCount <= 0 {
		keptRunCount = DefaultCompactKeptRunCount
	}
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return CompactSnapshot{}, err
	}
	if len(lines) == 0 || !isNewFormat(lines) {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	boundarySeq, boundaryRunID := compactBoundarySeq(lines, keptRunCount)
	if boundarySeq <= 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	if boundarySeq > len(lines) {
		boundarySeq = len(lines)
	}
	original := rawMessagesFromJSONLLines(lines[:boundarySeq])
	if len(original) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	projected, digests := digestToolResultMessages(original, DefaultCompactToolResultChars)
	allMessages := rawMessagesFromJSONLLines(lines)
	preMessages, _ := digestOlderToolResultMessages(allMessages, DefaultCompactToolResultChars, DefaultCompactRecentToolChars, DefaultCompactKeptRunCount)
	preTokens := estimateRawMessageTokens(limitRawMessagesByRuns(preMessages, 20))
	tail := rawMessagesFromJSONLLines(lines[boundarySeq:])
	tail, _ = digestOlderToolResultMessages(tail, DefaultCompactToolResultChars, DefaultCompactRecentToolChars, DefaultCompactKeptRunCount)
	compressionRatio := 1.0
	fallback := deterministicCompactSummary(projected, digests)
	postTokens := EstimateCompactPostTokens(fallback, "deterministic_fallback", 0, tail, 20)
	if preTokens > 0 {
		compressionRatio = float64(postTokens) / float64(preTokens)
	}
	compactedRunIDs := runIDsFromMessages(original)
	summaryTargetTokens := summaryTargetTokensForRunCount(len(compactedRunIDs))
	return CompactSnapshot{
		BoundarySeq:         boundarySeq,
		BoundaryRunID:       boundaryRunID,
		Generation:          latestCompactGeneration(lines) + 1,
		CompactedRunCount:   len(compactedRunIDs),
		OriginalMessages:    len(original),
		ProjectedMessages:   len(projected),
		PreCompactTokens:    preTokens,
		PostCompactTokens:   postTokens,
		CompressionRatio:    compressionRatio,
		SummaryTargetTokens: summaryTargetTokens,
		Prompt:              buildCompactPrompt(projected, digests, latestCompactGeneration(lines)+1, summaryTargetTokens),
		FallbackSummary:     fallback,
		ToolDigests:         digests,
		DigestedRunIDs:      runIDsFromToolDigests(digests),
		CacheMetrics:        latestCacheMetrics(lines[:boundarySeq]),
		TailMessages:        tail,
	}, nil
}

func EstimateCompactPostTokens(summaryText string, summarySource string, updatedAt int64, tail []map[string]any, k int) int {
	summary := map[string]any{
		"role":    "user",
		"content": compactSummaryMessage(&CompactLine{Summary: summaryText, SummarySource: summarySource, UpdatedAt: updatedAt}),
		"ts":      updatedAt,
	}
	projected := append([]map[string]any{summary}, limitRawMessagesByRuns(tail, k)...)
	return estimateRawMessageTokens(projected)
}

func (s *FileStore) LatestCompactLine(chatID string) (*CompactLine, error) {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	return latestCompactLineFromJSONLLines(lines), nil
}

func latestCompactLineFromJSONLLines(lines []map[string]any) *CompactLine {
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		lineType, _ := line["_type"].(string)
		if lineType != "compact" {
			continue
		}
		data, err := json.Marshal(line)
		if err != nil {
			continue
		}
		var compact CompactLine
		if err := json.Unmarshal(data, &compact); err != nil {
			continue
		}
		if strings.TrimSpace(compact.Summary) == "" {
			continue
		}
		return &compact
	}
	return nil
}

func compactBoundarySeq(lines []map[string]any, keptRunCount int) (int, string) {
	firstIndex := map[string]int{}
	var runOrder []string
	for index, line := range lines {
		lineType, _ := line["_type"].(string)
		if lineType == "compact" || lineType == "system" {
			continue
		}
		if strings.TrimSpace(stringValue(line["taskId"])) != "" || strings.TrimSpace(stringValue(line["taskSubAgentKey"])) != "" {
			continue
		}
		runID := strings.TrimSpace(stringValue(line["runId"]))
		if runID == "" {
			continue
		}
		if _, ok := firstIndex[runID]; ok {
			continue
		}
		firstIndex[runID] = index
		runOrder = append(runOrder, runID)
	}
	if len(runOrder) == 0 {
		return len(lines), ""
	}
	if len(runOrder) <= keptRunCount {
		return len(lines), runOrder[len(runOrder)-1]
	}
	boundaryRunID := runOrder[len(runOrder)-keptRunCount]
	return firstIndex[boundaryRunID], boundaryRunID
}

func rawMessagesWithCompactProjection(lines []map[string]any, k int) []map[string]any {
	compact := latestCompactLineFromJSONLLines(lines)
	if compact == nil {
		messages := rawMessagesFromJSONLLines(lines)
		messages, _ = digestOlderToolResultMessages(messages, DefaultCompactToolResultChars, DefaultCompactRecentToolChars, DefaultCompactKeptRunCount)
		return limitRawMessagesByRuns(messages, k)
	}
	boundarySeq := compact.BoundarySeq
	if boundarySeq < 0 {
		boundarySeq = 0
	}
	if boundarySeq > len(lines) {
		boundarySeq = len(lines)
	}
	tail := rawMessagesFromJSONLLines(lines[boundarySeq:])
	tail, _ = digestOlderToolResultMessages(tail, DefaultCompactToolResultChars, DefaultCompactRecentToolChars, DefaultCompactKeptRunCount)
	summary := map[string]any{
		"role":    "user",
		"content": compactSummaryMessage(compact),
		"ts":      compact.UpdatedAt,
	}
	return append([]map[string]any{summary}, limitRawMessagesByRuns(tail, k)...)
}

func compactSummaryMessage(compact *CompactLine) string {
	if compact == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("以下是此前对话的上下文压缩摘要。它替代 checkpoint 之前的原始历史；checkpoint 之后的消息仍按原文提供。\n\n")
	b.WriteString(strings.TrimSpace(compact.Summary))
	if compact.SummarySource != "" {
		b.WriteString("\n\n摘要来源: ")
		b.WriteString(compact.SummarySource)
	}
	return b.String()
}

func limitRawMessagesByRuns(messages []map[string]any, k int) []map[string]any {
	if k <= 0 {
		k = 20
	}
	if len(messages) == 0 {
		return nil
	}
	type runBucket struct {
		runID    string
		messages []map[string]any
	}
	var prefix []map[string]any
	var runs []*runBucket
	runIndex := map[string]*runBucket{}
	for _, msg := range messages {
		runID, _ := msg["runId"].(string)
		if runID == "" {
			prefix = append(prefix, msg)
			continue
		}
		bucket, ok := runIndex[runID]
		if !ok {
			bucket = &runBucket{runID: runID}
			runIndex[runID] = bucket
			runs = append(runs, bucket)
		}
		bucket.messages = append(bucket.messages, msg)
	}
	if len(runs) > k {
		runs = runs[len(runs)-k:]
	}
	result := append([]map[string]any(nil), prefix...)
	for _, bucket := range runs {
		result = append(result, bucket.messages...)
	}
	return result
}

func digestOlderToolResultMessages(messages []map[string]any, olderThreshold int, recentThreshold int, keepRunCount int) ([]map[string]any, []ToolDigest) {
	if len(messages) == 0 {
		return nil, nil
	}
	if olderThreshold <= 0 {
		olderThreshold = DefaultCompactToolResultChars
	}
	if recentThreshold <= 0 {
		recentThreshold = DefaultCompactRecentToolChars
	}
	keepRuns := recentRunIDs(messages, keepRunCount)
	copied := make([]map[string]any, 0, len(messages))
	var digests []ToolDigest
	for _, message := range messages {
		next := cloneRawMessage(message)
		runID, _ := next["runId"].(string)
		threshold := olderThreshold
		if _, keep := keepRuns[runID]; keep {
			threshold = recentThreshold
		}
		if digest, ok := digestToolMessageInPlace(next, threshold); ok {
			digests = append(digests, digest)
		}
		copied = append(copied, next)
	}
	return copied, digests
}

func digestToolResultMessages(messages []map[string]any, threshold int) ([]map[string]any, []ToolDigest) {
	copied := make([]map[string]any, 0, len(messages))
	var digests []ToolDigest
	for _, message := range messages {
		next := cloneRawMessage(message)
		if digest, ok := digestToolMessageInPlace(next, threshold); ok {
			digests = append(digests, digest)
		}
		copied = append(copied, next)
	}
	return copied, digests
}

func digestToolMessageInPlace(message map[string]any, threshold int) (ToolDigest, bool) {
	if threshold <= 0 {
		threshold = DefaultCompactToolResultChars
	}
	role, _ := message["role"].(string)
	if role != "tool" {
		return ToolDigest{}, false
	}
	content := stringValue(message["content"])
	if len(content) <= threshold {
		return ToolDigest{}, false
	}
	toolName := strings.TrimSpace(stringValue(message["name"]))
	toolCallID := strings.TrimSpace(stringValue(message["tool_call_id"]))
	runID := strings.TrimSpace(stringValue(message["runId"]))
	digestText, kind := digestToolResultText(toolName, content)
	sum := sha256.Sum256([]byte(content))
	message["content"] = digestText
	return ToolDigest{
		RunID:         runID,
		ToolCallID:    toolCallID,
		ToolName:      toolName,
		OriginalChars: len(content),
		DigestChars:   len(digestText),
		SHA256:        hex.EncodeToString(sum[:]),
		Kind:          kind,
	}, true
}

func digestToolResultText(toolName string, content string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(toolName))
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err == nil && len(payload) > 0 {
		switch {
		case isBashToolName(normalized) || hasAnyKey(payload, "stdout", "stderr", "exitCode", "workingDirectory"):
			return digestBashPayload(normalized, payload, content), "bash"
		case strings.Contains(normalized, "grep") || strings.Contains(normalized, "search"):
			return digestStructuredPayload(normalized, payload, content, "search"), "search"
		case strings.Contains(normalized, "read"):
			return digestStructuredPayload(normalized, payload, content, "file_read"), "file_read"
		case strings.Contains(normalized, "write") || strings.Contains(normalized, "edit"):
			return digestStructuredPayload(normalized, payload, content, "file_write"), "file_write"
		default:
			return digestStructuredPayload(normalized, payload, content, "structured"), "structured"
		}
	}
	return digestPlainText(normalized, content), "plain"
}

func digestBashPayload(toolName string, payload map[string]any, original string) string {
	stdout := stringValue(payload["stdout"])
	stderr := stringValue(payload["stderr"])
	exitCode := payload["exitCode"]
	cwd := stringValue(payload["workingDirectory"])
	if cwd == "" {
		cwd = stringValue(payload["cwd"])
	}
	var b strings.Builder
	b.WriteString("[工具结果已压缩]\n")
	b.WriteString("类型: bash\n")
	if toolName != "" {
		b.WriteString("工具: " + toolName + "\n")
	}
	if cwd != "" {
		b.WriteString("工作目录: " + cwd + "\n")
	}
	if exitCode != nil {
		b.WriteString(fmt.Sprintf("退出码: %v\n", exitCode))
	}
	b.WriteString(fmt.Sprintf("原始字符数: %d\n", len(original)))
	if stdout != "" {
		b.WriteString(fmt.Sprintf("stdout 行数: %d\nstdout 摘要:\n%s\n", lineCount(stdout), headTail(stdout, 900, 900)))
	}
	if stderr != "" {
		b.WriteString(fmt.Sprintf("stderr 行数: %d\nstderr 摘要:\n%s\n", lineCount(stderr), headTail(stderr, 600, 900)))
	}
	return strings.TrimSpace(b.String())
}

func digestStructuredPayload(toolName string, payload map[string]any, original string, kind string) string {
	keys := sortedMapKeys(payload)
	var b strings.Builder
	b.WriteString("[工具结果已压缩]\n")
	b.WriteString("类型: " + kind + "\n")
	if toolName != "" {
		b.WriteString("工具: " + toolName + "\n")
	}
	b.WriteString(fmt.Sprintf("原始字符数: %d\n", len(original)))
	if len(keys) > 0 {
		b.WriteString("字段: " + strings.Join(keys, ", ") + "\n")
	}
	for _, key := range []string{"path", "filePath", "file_path", "query", "range", "matches", "matchCount", "error", "message"} {
		value := strings.TrimSpace(stringValue(payload[key]))
		if value == "" {
			continue
		}
		b.WriteString(key + ": " + truncateMiddle(value, 1200) + "\n")
	}
	b.WriteString("内容摘要:\n")
	b.WriteString(headTail(original, defaultCompactSnippetHeadChars, defaultCompactSnippetTailChars))
	return strings.TrimSpace(b.String())
}

func digestPlainText(toolName string, content string) string {
	var b strings.Builder
	b.WriteString("[工具结果已压缩]\n")
	if toolName != "" {
		b.WriteString("工具: " + toolName + "\n")
	}
	b.WriteString(fmt.Sprintf("原始字符数: %d\n", len(content)))
	b.WriteString(fmt.Sprintf("行数: %d\n", lineCount(content)))
	b.WriteString("内容摘要:\n")
	b.WriteString(headTail(content, defaultCompactSnippetHeadChars, defaultCompactSnippetTailChars))
	return strings.TrimSpace(b.String())
}

func buildCompactPrompt(messages []map[string]any, digests []ToolDigest, generation int, summaryTargetTokens int) string {
	var b strings.Builder
	b.WriteString("请压缩以下智能体会话历史，用中文输出结构化摘要。摘要会替代旧历史进入后续模型上下文。\n")
	b.WriteString("必须保留: 用户目标、已完成事项、关键文件/命令、工具调用结论、失败与修复、当前状态、明确下一步。\n")
	b.WriteString("不要编造未发生的内容；如果信息缺失，写明未知。\n\n")
	if generation > 1 {
		b.WriteString(fmt.Sprintf("这是第 %d 次上下文压缩。本次输入来自原始 JSONL 历史重建，不要只概括旧摘要，要重新整合全部原始消息。\n", generation))
	}
	if summaryTargetTokens > 0 {
		b.WriteString(fmt.Sprintf("目标摘要长度约 %d tokens；如果关键信息较多，优先保留用户决策、错误修复映射和当前下一步。\n", summaryTargetTokens))
	}
	b.WriteString("输出结构必须包含: user_goal, key_decisions, completed_work, important_files_and_commands, failures_and_fixes, current_state, next_steps。\n\n")
	if len(digests) > 0 {
		b.WriteString("工具结果压缩统计:\n")
		for _, digest := range digests {
			b.WriteString(fmt.Sprintf("- tool=%s id=%s kind=%s originalChars=%d digestChars=%d sha256=%s\n",
				digest.ToolName, digest.ToolCallID, digest.Kind, digest.OriginalChars, digest.DigestChars, digest.SHA256))
		}
		b.WriteString("\n")
	}
	b.WriteString("会话历史:\n")
	b.WriteString(renderMessagesForCompact(messages))
	return truncateMiddle(b.String(), defaultCompactPromptMaxChars)
}

func deterministicCompactSummary(messages []map[string]any, digests []ToolDigest) string {
	var b strings.Builder
	b.WriteString("这是系统生成的确定性上下文摘要，模型摘要生成失败或不可用时使用。\n\n")
	b.WriteString("最近的关键消息:\n")
	start := len(messages) - 12
	if start < 0 {
		start = 0
	}
	for _, message := range messages[start:] {
		role := strings.TrimSpace(stringValue(message["role"]))
		content := strings.TrimSpace(stringValue(message["content"]))
		if content == "" && role == "assistant" {
			if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
				content = fmt.Sprintf("发起 %d 个工具调用", len(calls))
			}
		}
		if role == "" || content == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(truncateMiddle(content, 1200))
		b.WriteString("\n")
	}
	if len(digests) > 0 {
		b.WriteString("\n已压缩的大型工具结果:\n")
		for _, digest := range digests {
			b.WriteString(fmt.Sprintf("- %s(%s): %d -> %d chars, sha256=%s\n", digest.ToolName, digest.Kind, digest.OriginalChars, digest.DigestChars, digest.SHA256))
		}
	}
	return strings.TrimSpace(b.String())
}

func renderMessagesForCompact(messages []map[string]any) string {
	var b strings.Builder
	for _, message := range messages {
		role := strings.TrimSpace(stringValue(message["role"]))
		if role == "" {
			continue
		}
		b.WriteString("\n--- ")
		b.WriteString(role)
		if runID := strings.TrimSpace(stringValue(message["runId"])); runID != "" {
			b.WriteString(" runId=")
			b.WriteString(runID)
		}
		if name := strings.TrimSpace(stringValue(message["name"])); name != "" {
			b.WriteString(" tool=")
			b.WriteString(name)
		}
		b.WriteString(" ---\n")
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			data, _ := json.Marshal(calls)
			b.WriteString("tool_calls: ")
			b.WriteString(string(data))
			b.WriteString("\n")
		}
		content := strings.TrimSpace(stringValue(message["content"]))
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func latestCacheMetrics(lines []map[string]any) map[string]any {
	for index := len(lines) - 1; index >= 0; index-- {
		usage, _ := lines[index]["usage"].(map[string]any)
		if len(usage) == 0 {
			continue
		}
		out := map[string]any{}
		if value := intValue(usage["promptCacheHitTokens"]); value > 0 {
			out["promptCacheHitTokens"] = value
		}
		if value := intValue(usage["promptCacheMissTokens"]); value > 0 {
			out["promptCacheMissTokens"] = value
		}
		if details, _ := usage["promptTokensDetails"].(map[string]any); len(details) > 0 {
			if value := intValue(details["cachedTokens"]); value > 0 {
				out["cachedTokens"] = value
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func latestCompactGeneration(lines []map[string]any) int {
	latest := 0
	count := 0
	for _, line := range lines {
		if lineType, _ := line["_type"].(string); lineType != "compact" {
			continue
		}
		count++
		if value := intValue(line["generation"]); value > latest {
			latest = value
		}
	}
	if latest == 0 {
		return count
	}
	return latest
}

func estimateRawMessageTokens(messages []map[string]any) int {
	totalChars := 0
	for _, message := range messages {
		if data, err := json.Marshal(message); err == nil {
			totalChars += len(data)
			continue
		}
		totalChars += len(fmt.Sprint(message))
	}
	if totalChars == 0 {
		return 0
	}
	return totalChars/4 + 1
}

func summaryTargetTokensForRunCount(runCount int) int {
	target := runCount * 200
	if target < 1500 {
		target = 1500
	}
	if target > 6000 {
		target = 6000
	}
	return target
}

func runIDsFromMessages(messages []map[string]any) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, message := range messages {
		runID := strings.TrimSpace(stringValue(message["runId"]))
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		out = append(out, runID)
	}
	return out
}

func runIDsFromToolDigests(digests []ToolDigest) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, digest := range digests {
		runID := strings.TrimSpace(digest.RunID)
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		out = append(out, runID)
	}
	return out
}

func recentRunIDs(messages []map[string]any, keepRunCount int) map[string]struct{} {
	if keepRunCount <= 0 {
		return map[string]struct{}{}
	}
	var order []string
	seen := map[string]struct{}{}
	for _, message := range messages {
		runID := strings.TrimSpace(stringValue(message["runId"]))
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		order = append(order, runID)
	}
	if len(order) > keepRunCount {
		order = order[len(order)-keepRunCount:]
	}
	out := map[string]struct{}{}
	for _, runID := range order {
		out[runID] = struct{}{}
	}
	return out
}

func cloneRawMessage(message map[string]any) map[string]any {
	out := make(map[string]any, len(message))
	for key, value := range message {
		out[key] = value
	}
	return out
}

func isBashToolName(name string) bool {
	switch name {
	case "bash", "simple-bash", "sandbox_bash", "host_bash":
		return true
	default:
		return strings.Contains(name, "bash")
	}
}

func hasAnyKey(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func headTail(text string, headChars int, tailChars int) string {
	text = strings.TrimSpace(text)
	if len(text) <= headChars+tailChars+64 {
		return text
	}
	return text[:headChars] + "\n...[snip]...\n" + text[len(text)-tailChars:]
}

func truncateMiddle(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	half := maxChars / 2
	return text[:half] + "\n...[snip]...\n" + text[len(text)-(maxChars-half):]
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		v, _ := typed.Int64()
		return int(v)
	default:
		return 0
	}
}
