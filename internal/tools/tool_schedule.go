package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
)

// tool_schedule.go exposes three builtin tools that let an agent manage its own
// cron-driven follow-ups. Each schedule file is a plain YAML written into
// cfg.Paths.SchedulesDir so the schedule orchestrator's fsnotify watcher picks
// it up automatically. chatId ownership is the only isolation boundary — a
// schedule can only be listed/deleted by the chat that created it.

const scheduleFilePrefix = "sched_"

func (t *RuntimeToolExecutor) invokeScheduleCreate(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return scheduleErrorResult("schedule_create_missing_context", "execution context unavailable"), nil
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		return scheduleErrorResult("schedule_create_missing_chat", "chatId is required"), nil
	}
	agentKey := strings.TrimSpace(execCtx.Session.AgentKey)
	if agentKey == "" {
		return scheduleErrorResult("schedule_create_missing_agent", "agentKey is required"), nil
	}

	cronExpr := strings.TrimSpace(stringArg(args, "cron"))
	message := stringArg(args, "message")
	if cronExpr == "" {
		return scheduleErrorResult("schedule_create_invalid", "cron is required"), nil
	}
	if strings.TrimSpace(message) == "" {
		return scheduleErrorResult("schedule_create_invalid", "message is required"), nil
	}

	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		name = truncateForName(message, 40)
	}
	description := strings.TrimSpace(stringArg(args, "description"))
	if description == "" {
		description = name
	}
	zoneID := strings.TrimSpace(stringArg(args, "zoneId"))
	enabled := true
	if v, ok := args["enabled"].(bool); ok {
		enabled = v
	}

	schedulesDir := strings.TrimSpace(t.cfg.Paths.SchedulesDir)
	if schedulesDir == "" {
		return scheduleErrorResult("schedule_create_unconfigured", "schedules dir not configured"), nil
	}
	if err := os.MkdirAll(schedulesDir, 0o755); err != nil {
		return scheduleErrorResult("schedule_create_mkdir", err.Error()), nil
	}

	id, filePath, err := allocateScheduleFile(schedulesDir)
	if err != nil {
		return scheduleErrorResult("schedule_create_alloc", err.Error()), nil
	}

	yamlText := renderScheduleYAML(scheduleDoc{
		Name:        name,
		Description: description,
		Enabled:     enabled,
		Cron:        cronExpr,
		AgentKey:    agentKey,
		ZoneID:      zoneID,
		ChatID:      chatID,
		Message:     message,
	})
	if err := os.WriteFile(filePath, []byte(yamlText), 0o644); err != nil {
		return scheduleErrorResult("schedule_create_write", err.Error()), nil
	}

	return structuredResult(map[string]any{
		"status":      "created",
		"id":          id,
		"name":        name,
		"cron":        cronExpr,
		"chatId":      chatID,
		"agentKey":    agentKey,
		"enabled":     enabled,
		"file":        filepath.Base(filePath),
		"zoneId":      zoneID,
		"message":     message,
		"description": description,
	}), nil
}

func (t *RuntimeToolExecutor) invokeScheduleList(execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return scheduleErrorResult("schedule_list_missing_context", "execution context unavailable"), nil
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		return scheduleErrorResult("schedule_list_missing_chat", "chatId is required"), nil
	}

	items := listSchedulesForChat(t.cfg.Paths.SchedulesDir, chatID)
	return structuredResult(map[string]any{
		"status":    "ok",
		"chatId":    chatID,
		"schedules": items,
		"count":     len(items),
	}), nil
}

func (t *RuntimeToolExecutor) invokeScheduleDelete(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return scheduleErrorResult("schedule_delete_missing_context", "execution context unavailable"), nil
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		return scheduleErrorResult("schedule_delete_missing_chat", "chatId is required"), nil
	}
	targetID := strings.TrimSpace(stringArg(args, "id"))
	if targetID == "" {
		return scheduleErrorResult("schedule_delete_invalid", "id is required"), nil
	}

	schedulesDir := strings.TrimSpace(t.cfg.Paths.SchedulesDir)
	if schedulesDir == "" {
		return scheduleErrorResult("schedule_delete_unconfigured", "schedules dir not configured"), nil
	}

	filePath, ownerChatID, err := findScheduleFile(schedulesDir, targetID)
	if err != nil {
		return scheduleErrorResult("schedule_delete_lookup", err.Error()), nil
	}
	if filePath == "" {
		return scheduleErrorResult("schedule_delete_not_found", "schedule not found"), nil
	}
	if ownerChatID != chatID {
		return scheduleErrorResult("schedule_delete_forbidden", "schedule belongs to another chat"), nil
	}
	if err := os.Remove(filePath); err != nil {
		return scheduleErrorResult("schedule_delete_remove", err.Error()), nil
	}
	return structuredResult(map[string]any{
		"status": "deleted",
		"id":     targetID,
		"chatId": chatID,
	}), nil
}

type scheduleDoc struct {
	Name        string
	Description string
	Enabled     bool
	Cron        string
	AgentKey    string
	ZoneID      string
	ChatID      string
	Message     string
}

// renderScheduleYAML 输出和 zenmind-env/schedules 人工示例同构的 YAML：
// - name / description / agentKey / zoneId：明文输出（短字符串，一行）
// - cron / chatId / message：双引号或 block scalar（带空白、特殊字符或多行）
// 解析端用 internal/config/yaml_tree 自定义 YAML 解析器，其 parseYAMLScalar 会
// strip 外层引号但不解析 \n 这类转义，所以含换行的值必须走 block scalar，而
// 不能塞成 "foo\nbar"。
func renderScheduleYAML(d scheduleDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", yamlInlineValue(d.Name))
	fmt.Fprintf(&b, "description: %s\n", yamlInlineValue(d.Description))
	fmt.Fprintf(&b, "enabled: %t\n", d.Enabled)
	fmt.Fprintf(&b, "cron: %s\n", yamlQuotedValue(d.Cron))
	fmt.Fprintf(&b, "agentKey: %s\n", yamlInlineValue(d.AgentKey))
	if d.ZoneID != "" {
		b.WriteString("environment:\n")
		fmt.Fprintf(&b, "  zoneId: %s\n", yamlInlineValue(d.ZoneID))
	}
	b.WriteString("query:\n")
	fmt.Fprintf(&b, "  chatId: %s\n", yamlQuotedValue(d.ChatID))
	writeYAMLField(&b, "message", d.Message, 2)
	return b.String()
}

// writeYAMLField 为可能含换行的长字段（description / message）选择合适表达：
// - 含换行：block scalar `|-` + 统一缩进
// - 单行：退回双引号形态
func writeYAMLField(b *strings.Builder, key string, value string, parentIndent int) {
	indent := strings.Repeat(" ", parentIndent)
	if strings.ContainsAny(value, "\n\r") {
		fmt.Fprintf(b, "%s%s: |-\n", indent, key)
		childIndent := strings.Repeat(" ", parentIndent+2)
		for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
			fmt.Fprintf(b, "%s%s\n", childIndent, line)
		}
		return
	}
	fmt.Fprintf(b, "%s%s: %s\n", indent, key, yamlQuotedValue(value))
}

// yamlInlineValue 在值是"简单字符串"（没有会让 YAML 解析歧义的字符）时直接
// 明文输出，否则走双引号。
func yamlInlineValue(raw string) string {
	if isSimpleYAMLScalar(raw) {
		return raw
	}
	return yamlQuotedValue(raw)
}

// yamlQuotedValue 把单行字符串包成双引号形式，只转义反斜杠和引号本身。
// 调用方必须保证 raw 不含换行（含换行请用 writeYAMLField）。
func yamlQuotedValue(raw string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
	).Replace(raw)
	return `"` + escaped + `"`
}

func isSimpleYAMLScalar(raw string) bool {
	if raw == "" {
		return false
	}
	if strings.TrimSpace(raw) != raw {
		return false
	}
	switch strings.ToLower(raw) {
	case "true", "false", "null", "~", "yes", "no", "on", "off":
		return false
	}
	for _, ch := range raw {
		switch ch {
		case ':', '#', '"', '\'', '{', '}', '[', ']', ',', '&', '*', '!', '|', '>', '%', '@', '`':
			return false
		}
	}
	return true
}

// allocateScheduleFile builds a unique schedule id derived from base36 epoch
// millis plus a short random suffix to avoid collisions when an agent issues
// multiple create calls inside the same millisecond.
func allocateScheduleFile(dir string) (string, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 36)
		rnd, err := randomHex(3)
		if err != nil {
			return "", "", err
		}
		id := scheduleFilePrefix + ts + "_" + rnd
		path := filepath.Join(dir, id+".yml")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return id, path, nil
		}
	}
	return "", "", fmt.Errorf("cannot allocate unique schedule id after 16 attempts")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type scheduleListItem struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Cron        string `json:"cron"`
	Message     string `json:"message"`
	Enabled     bool   `json:"enabled"`
	AgentKey    string `json:"agentKey,omitempty"`
	ChatID      string `json:"chatId"`
	File        string `json:"file"`
}

func listSchedulesForChat(root string, chatID string) []scheduleListItem {
	root = strings.TrimSpace(root)
	if root == "" || chatID == "" {
		return nil
	}
	var items []scheduleListItem
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !isYAMLFile(path) {
			return nil
		}
		item, ok := readScheduleItem(path)
		if !ok || item.ChatID != chatID {
			return nil
		}
		items = append(items, item)
		return nil
	})
	return items
}

func findScheduleFile(root string, targetID string) (string, string, error) {
	root = strings.TrimSpace(root)
	if root == "" || targetID == "" {
		return "", "", nil
	}
	var (
		foundPath   string
		foundChatID string
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !isYAMLFile(path) {
			return nil
		}
		item, ok := readScheduleItem(path)
		if !ok || item.ID != targetID {
			return nil
		}
		foundPath = path
		foundChatID = item.ChatID
		return filepath.SkipAll
	})
	if err != nil {
		return "", "", err
	}
	return foundPath, foundChatID, nil
}

func readScheduleItem(path string) (scheduleListItem, bool) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return scheduleListItem{}, false
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return scheduleListItem{}, false
	}
	query, _ := root["query"].(map[string]any)
	if query == nil {
		return scheduleListItem{}, false
	}
	chatID := strings.TrimSpace(AnyStringNode(query["chatId"]))
	if chatID == "" {
		return scheduleListItem{}, false
	}
	base := filepath.Base(path)
	id := strings.TrimSuffix(base, filepath.Ext(base))

	enabled := true
	if v, ok := root["enabled"].(bool); ok {
		enabled = v
	}
	return scheduleListItem{
		ID:          id,
		Name:        AnyStringNode(root["name"]),
		Description: AnyStringNode(root["description"]),
		Cron:        AnyStringNode(root["cron"]),
		AgentKey:    AnyStringNode(root["agentKey"]),
		Message:     AnyStringNode(query["message"]),
		ChatID:      chatID,
		Enabled:     enabled,
		File:        base,
	}, true
}

func isYAMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
}

func truncateForName(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "schedule"
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func scheduleErrorResult(code string, message string) ToolExecutionResult {
	payload := map[string]any{
		"status":  "error",
		"code":    code,
		"message": message,
	}
	return ToolExecutionResult{
		Output:     MarshalJSON(payload),
		Structured: payload,
		Error:      code,
		ExitCode:   -1,
	}
}
