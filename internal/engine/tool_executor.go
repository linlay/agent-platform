package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

type RuntimeToolExecutor struct {
	cfg     config.Config
	sandbox SandboxClient
	memory  memory.Store
	defs    []api.ToolDetailResponse
}

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient, memoryStore memory.Store) (*RuntimeToolExecutor, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if !cfg.ContainerHub.Enabled && strings.EqualFold(strings.TrimSpace(def.Name), "_sandbox_bash_") {
			continue
		}
		filtered = append(filtered, def)
	}
	return &RuntimeToolExecutor{
		cfg:     cfg,
		sandbox: sandbox,
		memory:  memoryStore,
		defs:    filtered,
	}, nil
}

func (t *RuntimeToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), t.defs...)
}

func (t *RuntimeToolExecutor) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	switch strings.TrimSpace(toolName) {
	case "_datetime_":
		return t.invokeDateTime(args), nil
	case "_artifact_publish_":
		return t.invokeArtifactPublish(args, execCtx)
	case "_plan_add_tasks_":
		return t.invokePlanAddTasks(args, execCtx)
	case "_plan_get_tasks_":
		return t.invokePlanGetTasks(execCtx)
	case "_plan_update_task_":
		return t.invokePlanUpdateTask(args, execCtx)
	case "_bash_":
		return t.invokeHostBash(ctx, args)
	case "_sandbox_bash_":
		return t.invokeSandboxBash(ctx, args, execCtx)
	case "_memory_search_", "memory_search":
		return t.invokeMemorySearch(toolName, args, execCtx)
	case "_memory_read_", "memory_read":
		return t.invokeMemoryRead(toolName, args, execCtx)
	case "_memory_write_", "memory_write":
		return t.invokeMemoryWrite(toolName, args, execCtx)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func (t *RuntimeToolExecutor) invokeMemorySearch(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	items, err := t.memory.SearchDetailed(agentKey, query, stringArg(args, "category"), memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit))
	if err != nil {
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, map[string]any{
			"memory":    memoryToolRecordValue(item.Memory),
			"score":     item.Score,
			"matchType": item.MatchType,
		})
	}
	payload := map[string]any{"results": results, "count": len(results)}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeMemoryRead(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	id := stringArg(args, "id")
	if id != "" {
		item, err := t.memory.ReadDetail(agentKey, id)
		if err != nil {
			return ToolExecutionResult{}, err
		}
		if item == nil {
			return structuredResult(map[string]any{"found": false}), nil
		}
		return structuredResult(map[string]any{"found": true, "memory": memoryToolRecordValue(*item)}), nil
	}
	items, err := t.memory.List(agentKey, stringArg(args, "category"), memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit), stringArg(args, "sort"))
	if err != nil {
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, memoryToolRecordValue(item))
	}
	return structuredResult(map[string]any{"count": len(results), "results": results}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryWrite(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, requestID, chatID, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	content := strings.TrimSpace(stringArg(args, "content"))
	if content == "" {
		return ToolExecutionResult{Output: "content must not be blank", Error: "missing_content", ExitCode: -1}, nil
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         fmt.Sprintf("mem_%d", time.Now().UnixNano()),
		RequestID:  requestID,
		ChatID:     chatID,
		AgentKey:   agentKey,
		SubjectKey: normalizeMemorySubjectKey("", chatID, agentKey),
		Summary:    content,
		SourceType: normalizeMemorySourceType("tool-write"),
		Category:   normalizeMemoryCategory(stringArg(args, "category")),
		Importance: normalizeMemoryImportance(int(int64Arg(args, "importance"))),
		Tags:       normalizeMemoryTags(stringListArg(args, "tags")),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := t.memory.Write(item); err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"id":           item.ID,
		"status":       "stored",
		"subjectKey":   item.SubjectKey,
		"sourceType":   item.SourceType,
		"category":     item.Category,
		"importance":   item.Importance,
		"hasEmbedding": false,
	}), nil
}

func (t *RuntimeToolExecutor) invokeDateTime(args map[string]any) ToolExecutionResult {
	payload, err := buildDateTimePayload(args, time.Now())
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "invalid_datetime_arguments", ExitCode: -1}
	}
	return structuredResult(payload)
}

func (t *RuntimeToolExecutor) invokeArtifactPublish(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	artifacts, _ := args["artifacts"]
	published := make([]map[string]any, 0)
	if execCtx != nil {
		log.Printf("[artifact-publish] chatsDir=%s chatID=%s runID=%s artifacts=%v",
			t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, execCtx.Session.RunID, artifacts)
		published = publishArtifacts(t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, execCtx.Session.RunID, artifacts)
		log.Printf("[artifact-publish] published=%d items=%v", len(published), published)
	}
	payload := map[string]any{
		"status":             "published",
		"artifacts":          artifacts,
		"publishedArtifacts": published,
	}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokePlanAddTasks(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	state := ensurePlanState(execCtx)
	var tasks []PlanTask
	if rawTasks, ok := args["tasks"].([]any); ok {
		for _, item := range rawTasks {
			taskMap, _ := item.(map[string]any)
			description := anyStringNode(taskMap["description"])
			if strings.TrimSpace(description) == "" {
				continue
			}
			taskID := anyStringNode(taskMap["taskId"])
			if strings.TrimSpace(taskID) == "" {
				taskID = shortPlanID()
			}
			tasks = append(tasks, PlanTask{
				TaskID:      taskID,
				Description: strings.TrimSpace(description),
				Status:      normalizePlanTaskStatus(anyStringNode(taskMap["status"])),
			})
		}
	}
	if len(tasks) == 0 {
		description := anyStringNode(args["description"])
		if strings.TrimSpace(description) == "" {
			return ToolExecutionResult{Output: "失败: 缺少任务描述", Error: "missing_task_description", ExitCode: -1}, nil
		}
		taskID := anyStringNode(args["taskId"])
		if strings.TrimSpace(taskID) == "" {
			taskID = shortPlanID()
		}
		tasks = append(tasks, PlanTask{
			TaskID:      taskID,
			Description: strings.TrimSpace(description),
			Status:      normalizePlanTaskStatus(anyStringNode(args["status"])),
		})
	}
	if state.PlanID == "" {
		state.PlanID = execCtx.Session.RunID + "_plan"
	}
	state.Tasks = append(state.Tasks, tasks...)
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		lines = append(lines, task.TaskID+" | "+task.Status+" | "+task.Description)
	}
	return ToolExecutionResult{
		Output:     strings.Join(lines, "\n"),
		Structured: planStatePayload(state),
		ExitCode:   0,
	}, nil
}

func (t *RuntimeToolExecutor) invokePlanGetTasks(execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil || execCtx.PlanState == nil {
		payload := NewErrorPayload("plan_context_unavailable", "Plan context is unavailable in direct invocation", ErrorScopeRun, ErrorCategorySystem, nil)
		return ToolExecutionResult{Output: marshalJSON(payload), Structured: payload, Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	return structuredResult(planStatePayload(execCtx.PlanState)), nil
}

func (t *RuntimeToolExecutor) invokePlanUpdateTask(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	state := ensurePlanState(execCtx)
	taskID := anyStringNode(args["taskId"])
	if strings.TrimSpace(taskID) == "" {
		return ToolExecutionResult{Output: "失败: 缺少 taskId", Error: "missing_task_id", ExitCode: -1}, nil
	}
	status := normalizePlanTaskStatus(anyStringNode(args["status"]))
	if status == "" {
		return ToolExecutionResult{Output: "失败: 非法状态，仅支持 init/in_progress/completed/failed/canceled", Error: "invalid_task_status", ExitCode: -1}, nil
	}
	for index := range state.Tasks {
		if strings.TrimSpace(state.Tasks[index].TaskID) != strings.TrimSpace(taskID) {
			continue
		}
		state.Tasks[index].Status = status
		if state.ActiveTaskID == taskID && (status == "completed" || status == "failed" || status == "canceled") {
			state.ActiveTaskID = ""
		}
		return ToolExecutionResult{Output: "OK", Structured: planStatePayload(state), ExitCode: 0}, nil
	}
	return ToolExecutionResult{Output: "失败: taskId 不存在", Error: "task_not_found", ExitCode: -1}, nil
}

func (t *RuntimeToolExecutor) invokeSandboxBash(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	timeoutMs := int64Arg(args, "timeout_ms")
	result, err := t.sandbox.Execute(ctx, execCtx, command, stringArg(args, "cwd"), timeoutMs)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "sandbox_execute_failed", ExitCode: -1}, nil
	}
	// Java: success (exitCode=0, no stderr) → plain text stdout
	//       failure → JSON error object
	if result.ExitCode == 0 && strings.TrimSpace(result.Stderr) == "" {
		return ToolExecutionResult{Output: result.Stdout, ExitCode: 0}, nil
	}
	payload := map[string]any{
		"exitCode":         result.ExitCode,
		"mode":             "sandbox",
		"workingDirectory": result.WorkingDirectory,
		"stdout":           result.Stdout,
		"stderr":           result.Stderr,
	}
	return structuredResultWithExit(payload, result.ExitCode), nil
}

func (t *RuntimeToolExecutor) invokeHostBash(ctx context.Context, args map[string]any) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	if len(command) > maxInt(t.cfg.Bash.MaxCommandChars, 16000) {
		return ToolExecutionResult{Output: "Command is too long", Error: "command_too_long", ExitCode: -1}, nil
	}
	if len(t.cfg.Bash.AllowedCommands) == 0 {
		return ToolExecutionResult{Output: "Bash command whitelist is empty", Error: "command_whitelist_empty", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled {
		if err := validateStrictCommand(command, t.cfg.Bash); err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "command_not_allowed", ExitCode: -1}, nil
		}
	}

	timeout := time.Duration(maxInt(t.cfg.Bash.ShellTimeoutMs, 30000)) * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellExecutable := strings.TrimSpace(t.cfg.Bash.ShellExecutable)
	if shellExecutable == "" {
		shellExecutable = "bash"
	}
	cmd := exec.CommandContext(runCtx, shellExecutable, "-lc", command)
	workingDir := t.cfg.Bash.WorkingDirectory
	if workingDir == "" {
		workingDir = "."
	}
	cmd.Dir = workingDir
	output, err := cmd.CombinedOutput()
	exitCode := 0
	stderr := ""
	stdout := string(output)
	if len(stdout) > maxBashOutputChars {
		stdout = stdout[:maxBashOutputChars]
	}
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		stderr = err.Error()
		if runCtx.Err() == context.DeadlineExceeded {
			stderr = "Command timed out"
		}
	}
	payload := map[string]any{
		"exitCode":         exitCode,
		"mode":             "host",
		"workingDirectory": workingDir,
		"stdout":           stdout,
		"stderr":           stderr,
	}
	return structuredResultWithExit(payload, exitCode), nil
}

// Hardcoded unsupported commands blacklist (Java: SystemBash.UNSUPPORTED_COMMANDS)
var unsupportedBashCommands = map[string]bool{
	".": true, "source": true, "eval": true, "exec": true,
	"coproc": true, "fg": true, "bg": true, "jobs": true,
}

const maxBashOutputChars = 8000 // Java: MAX_OUTPUT_CHARS = 8_000

func validateStrictCommand(command string, cfg config.BashConfig) error {
	if strings.ContainsAny(command, "\n;&|<>(){}") {
		return fmt.Errorf("Unsupported syntax for _bash_")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("Cannot parse command")
	}
	base := fields[0]
	if unsupportedBashCommands[strings.ToLower(base)] {
		return fmt.Errorf("Unsupported command: %s", base)
	}
	if !containsString(cfg.AllowedCommands, base) {
		return fmt.Errorf("Command not allowed: %s", base)
	}
	if !containsString(cfg.PathCheckedCommands, base) || containsString(cfg.PathCheckBypassCommands, base) {
		return nil
	}
	workingDirectory := cfg.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		if !looksLikePathArg(field) {
			continue
		}
		resolved := field
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workingDirectory, resolved)
		}
		resolved = filepath.Clean(resolved)
		if !pathAllowed(resolved, cfg.AllowedPaths, workingDirectory) {
			return fmt.Errorf("Path not allowed: %s", field)
		}
	}
	return nil
}

func looksLikePathArg(arg string) bool {
	return strings.Contains(arg, "/") || strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "~")
}

func pathAllowed(resolved string, allowed []string, workingDirectory string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, root := range allowed {
		if root == "" {
			continue
		}
		checkRoot := root
		if !filepath.IsAbs(checkRoot) {
			checkRoot = filepath.Join(workingDirectory, checkRoot)
		}
		checkRoot = filepath.Clean(checkRoot)
		if resolved == checkRoot || strings.HasPrefix(resolved, checkRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	default:
		return 0
	}
}

func defaultStringArg(args map[string]any, key string, fallback string) string {
	if value := stringArg(args, key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	return structuredResultWithExit(payload, 0)
}

func structuredResultWithExit(payload map[string]any, exitCode int) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   exitCode,
	}
}

func ensurePlanState(execCtx *ExecutionContext) *PlanRuntimeState {
	if execCtx.PlanState == nil {
		execCtx.PlanState = &PlanRuntimeState{
			PlanID: execCtx.Session.RunID + "_plan",
		}
	}
	return execCtx.PlanState
}

// planTasksArray returns just the tasks array for SSE plan events.
// The frontend reads event.plan as List<PlanTask> directly.
func planTasksArray(state *PlanRuntimeState) []map[string]any {
	if state == nil {
		return []map[string]any{}
	}
	tasks := make([]map[string]any, 0, len(state.Tasks))
	for _, task := range state.Tasks {
		tasks = append(tasks, map[string]any{
			"taskId":      task.TaskID,
			"description": task.Description,
			"status":      task.Status,
		})
	}
	return tasks
}

func planStatePayload(state *PlanRuntimeState) map[string]any {
	if state == nil {
		return map[string]any{
			"plan": []map[string]any{},
		}
	}
	tasks := make([]map[string]any, 0, len(state.Tasks))
	for _, task := range state.Tasks {
		tasks = append(tasks, map[string]any{
			"taskId":      task.TaskID,
			"description": task.Description,
			"status":      task.Status,
		})
	}
	payload := map[string]any{
		"planId": state.PlanID,
		"plan":   tasks,
	}
	if state.ActiveTaskID != "" {
		payload["currentTaskId"] = state.ActiveTaskID
	}
	return payload
}

var planTaskCounter atomic.Int64

func shortPlanID() string {
	seq := planTaskCounter.Add(1)
	return fmt.Sprintf("task_%d_%d", time.Now().UnixMilli(), seq)
}

// publishArtifacts resolves artifact paths from the sandbox /workspace to the
// local chat directory, copies files into artifacts/<runId>/, and returns
// publication metadata. Mirrors Java ArtifactPublishService.publish().
func publishArtifacts(chatsRoot string, chatID string, runID string, raw any) []map[string]any {
	if strings.TrimSpace(chatsRoot) == "" || strings.TrimSpace(chatID) == "" {
		return nil
	}
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil
	}
	chatDir := filepath.Join(chatsRoot, chatID)
	artifactsDir := filepath.Join(chatDir, "artifacts", runID)
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil
	}
	published := make([]map[string]any, 0, len(items))
	for index, item := range items {
		// Support both {"path": "/workspace/file"} and plain "/workspace/file"
		var rawPath string
		var mapped map[string]any
		switch v := item.(type) {
		case map[string]any:
			mapped = v
			rawPath = anyStringNode(v["path"])
		case string:
			rawPath = strings.TrimSpace(v)
			mapped = map[string]any{"path": rawPath}
		default:
			continue
		}
		if rawPath == "" {
			continue
		}

		// Resolve source path: /workspace/... → chatsDir/chatID/...
		sourcePath := resolveArtifactSourcePath(rawPath, chatDir)
		if sourcePath == "" {
			log.Printf("[artifact-publish] skip: path resolve failed rawPath=%s chatDir=%s", rawPath, chatDir)
			continue
		}
		info, err := os.Stat(sourcePath)
		if err != nil || info.IsDir() {
			log.Printf("[artifact-publish] skip: file not found sourcePath=%s err=%v", sourcePath, err)
			continue
		}

		artifactID := anyStringNode(mapped["artifactId"])
		if artifactID == "" {
			artifactID = fmt.Sprintf("artifact_%d_%d", time.Now().UnixMilli(), index)
		}

		// Determine display name from path or explicit name
		name := anyStringNode(mapped["name"])
		if name == "" {
			name = filepath.Base(sourcePath)
		}
		filename := filepath.Base(name)

		// Copy file into artifacts dir with dedup (Java: materializeIntoChatAssets)
		targetPath := deduplicateTargetPath(artifactsDir, filename, sourcePath)
		if !strings.HasPrefix(filepath.Clean(sourcePath), filepath.Clean(artifactsDir)) {
			if copyErr := copyFile(sourcePath, targetPath); copyErr != nil {
				continue
			}
		} else {
			targetPath = sourcePath
		}

		sha256hex := sha256Hex(targetPath)
		publishedFilename := filepath.Base(targetPath)
		relPath := filepath.ToSlash(filepath.Join(chatID, "artifacts", runID, publishedFilename))
		published = append(published, map[string]any{
			"artifactId": artifactID,
			"name":       publishedFilename,
			"mimeType":   guessMimeType(publishedFilename),
			"sizeBytes":  info.Size(),
			"sha256":     sha256hex,
			"url":        "/api/resource?file=" + relPath,
			"type":       defaultStringArg(mapped, "type", "file"),
		})
	}
	return published
}

// resolveArtifactSourcePath maps sandbox paths (/workspace/...) to local paths.
// Java: ArtifactPublishService.resolveSourcePath()
func resolveArtifactSourcePath(rawPath string, chatDir string) string {
	const sandboxPrefix = "/workspace"
	normalized := strings.TrimSpace(rawPath)
	if strings.HasPrefix(normalized, sandboxPrefix) {
		suffix := strings.TrimPrefix(normalized, sandboxPrefix)
		suffix = strings.TrimLeft(suffix, "/")
		if suffix == "" {
			return chatDir
		}
		resolved := filepath.Clean(filepath.Join(chatDir, suffix))
		if !strings.HasPrefix(resolved, filepath.Clean(chatDir)) {
			return "" // path escapes chat dir
		}
		return resolved
	}
	// Relative path → resolve relative to chat dir
	if !filepath.IsAbs(normalized) {
		resolved := filepath.Clean(filepath.Join(chatDir, normalized))
		if !strings.HasPrefix(resolved, filepath.Clean(chatDir)) {
			return ""
		}
		return resolved
	}
	// Absolute path — must be inside chat dir
	resolved := filepath.Clean(normalized)
	if !strings.HasPrefix(resolved, filepath.Clean(chatDir)) {
		return ""
	}
	return resolved
}

// deduplicateTargetPath returns a target path with counter suffix if filename
// already exists with different content (Java: materializeIntoChatAssets).
func deduplicateTargetPath(dir string, filename string, sourcePath string) string {
	baseName := filename
	ext := ""
	if dotIdx := strings.LastIndex(filename, "."); dotIdx > 0 {
		baseName = filename[:dotIdx]
		ext = filename[dotIdx:]
	}
	counter := 0
	for {
		candidateName := filename
		if counter > 0 {
			candidateName = fmt.Sprintf("%s-%d%s", baseName, counter, ext)
		}
		candidate := filepath.Join(dir, candidateName)
		info, err := os.Stat(candidate)
		if err != nil {
			// Doesn't exist yet — use this name
			return candidate
		}
		// Exists — check if same content
		if info.Mode().IsRegular() && sameFileContent(sourcePath, candidate) {
			return candidate
		}
		counter++
	}
}

func sameFileContent(left string, right string) bool {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false
	}
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false
	}
	rightData, err := os.ReadFile(right)
	if err != nil {
		return false
	}
	return string(leftData) == string(rightData)
}

func sha256Hex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

func copyFile(src string, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func guessMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	case ".json":
		return "application/json"
	case ".zip":
		return "application/zip"
	case ".md":
		return "text/markdown"
	default:
		return "application/octet-stream"
	}
}

func normalizePlanTaskStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "init":
		return "init"
	case "in_progress", "in-progress", "inprogress":
		return "in_progress"
	case "completed", "complete":
		return "completed"
	case "failed", "fail":
		return "failed"
	case "canceled", "cancelled", "cancel":
		return "canceled"
	default:
		return ""
	}
}
