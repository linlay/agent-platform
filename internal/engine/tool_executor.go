package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient, memoryStore memory.Store) *RuntimeToolExecutor {
	defs := []api.ToolDetailResponse{
		bashToolDefinition(),
		dateTimeToolDefinition(),
		artifactPublishToolDefinition(),
		planAddTasksToolDefinition(),
		planGetTasksToolDefinition(),
		planUpdateTaskToolDefinition(),
		memorySearchToolDefinition(),
		memoryReadToolDefinition(),
		memoryWriteToolDefinition(),
	}
	if cfg.ContainerHub.Enabled {
		defs = append(defs, sandboxBashToolDefinition())
	}
	return &RuntimeToolExecutor{
		cfg:     cfg,
		sandbox: sandbox,
		memory:  memoryStore,
		defs:    defs,
	}
}

func (t *RuntimeToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), t.defs...)
}

func (t *RuntimeToolExecutor) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	switch strings.TrimSpace(toolName) {
	case "_datetime_":
		return t.invokeDateTime(), nil
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
	case "memory_search":
		return t.invokeMemorySearch(args)
	case "memory_read":
		return t.invokeMemoryRead(args)
	case "memory_write":
		return t.invokeMemoryWrite(args)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func (t *RuntimeToolExecutor) invokeMemorySearch(args map[string]any) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	items, err := t.memory.Search(stringArg(args, "query"), int(int64Arg(args, "limit")))
	if err != nil {
		return ToolExecutionResult{}, err
	}
	payload := map[string]any{"items": items, "count": len(items)}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeMemoryRead(args map[string]any) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	id := stringArg(args, "id")
	if id == "" {
		return ToolExecutionResult{Output: "Missing argument: id", Error: "missing_id", ExitCode: -1}, nil
	}
	item, err := t.memory.Read(id)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	if item == nil {
		return ToolExecutionResult{Output: "memory item not found", Error: "memory_not_found", ExitCode: -1}, nil
	}
	return structuredResult(map[string]any{"item": item}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryWrite(args map[string]any) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	item := api.StoredMemoryResponse{
		ID:         stringArg(args, "id"),
		ChatID:     stringArg(args, "chatId"),
		SubjectKey: stringArg(args, "subjectKey"),
		Summary:    stringArg(args, "summary"),
		SourceType: defaultStringArg(args, "sourceType", "tool"),
		Category:   defaultStringArg(args, "category", "tool"),
		Importance: int(int64Arg(args, "importance")),
		CreatedAt:  time.Now().UnixMilli(),
		UpdatedAt:  time.Now().UnixMilli(),
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	if item.ChatID == "" || strings.TrimSpace(item.Summary) == "" {
		return ToolExecutionResult{Output: "Missing required memory fields", Error: "missing_memory_fields", ExitCode: -1}, nil
	}
	if err := t.memory.Write(item); err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{"item": item, "status": "stored"}), nil
}

func (t *RuntimeToolExecutor) invokeDateTime() ToolExecutionResult {
	now := time.Now()
	payload := map[string]any{
		"iso8601": now.Format(time.RFC3339),
		"unixMs":  now.UnixMilli(),
		"zone":    now.Location().String(),
	}
	return structuredResult(payload)
}

func (t *RuntimeToolExecutor) invokeArtifactPublish(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	artifacts, _ := args["artifacts"]
	published := make([]map[string]any, 0)
	if execCtx != nil {
		published = publishArtifacts(t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, artifacts)
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

func validateStrictCommand(command string, cfg config.BashConfig) error {
	if strings.ContainsAny(command, "\n;&|<>(){}") {
		return fmt.Errorf("Unsupported syntax for _bash_")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("Cannot parse command")
	}
	base := fields[0]
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

func shortPlanID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}

func publishArtifacts(chatsRoot string, chatID string, raw any) []map[string]any {
	if strings.TrimSpace(chatsRoot) == "" || strings.TrimSpace(chatID) == "" {
		return nil
	}
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil
	}
	artifactsDir := filepath.Join(chatsRoot, chatID, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil
	}
	published := make([]map[string]any, 0, len(items))
	for index, item := range items {
		mapped, _ := item.(map[string]any)
		if mapped == nil {
			continue
		}
		artifactID := anyStringNode(mapped["artifactId"])
		if artifactID == "" {
			artifactID = fmt.Sprintf("artifact_%d_%d", time.Now().UnixNano(), index)
		}
		name := anyStringNode(mapped["name"])
		if name == "" {
			name = artifactID + ".json"
		}
		filename := filepath.Base(name)
		if !strings.Contains(filename, ".") {
			filename += ".json"
		}
		targetPath := filepath.Join(artifactsDir, filename)
		data, err := json.MarshalIndent(mapped, "", "  ")
		if err != nil {
			continue
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			continue
		}
		published = append(published, map[string]any{
			"artifactId": artifactID,
			"name":       filename,
			"path":       targetPath,
			"url":        "/api/resource?file=" + filepath.ToSlash(filepath.Join(chatID, "artifacts", filename)),
			"type":       defaultStringArg(mapped, "type", "file"),
		})
	}
	return published
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

func dateTimeToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_datetime_",
		Name:        "_datetime_",
		Label:       "日期时间",
		Description: "获取当前或偏移后的日期时间",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_datetime_"},
	}
}

func artifactPublishToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_artifact_publish_",
		Name:        "_artifact_publish_",
		Label:       "发布产物",
		Description: "发布当前运行中生成的产物",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifacts": map[string]any{"type": "array"},
			},
			"required": []string{"artifacts"},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_artifact_publish_"},
	}
}

func planAddTasksToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_plan_add_tasks_",
		Name:        "_plan_add_tasks_",
		Label:       "创建计划任务",
		Description: "创建计划任务（追加模式），支持一次添加多个任务。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string"},
				"tasks": map[string]any{
					"type": "array",
				},
			},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_plan_add_tasks_", "clientVisible": false},
	}
}

func planGetTasksToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_plan_get_tasks_",
		Name:        "_plan_get_tasks_",
		Label:       "读取计划任务",
		Description: "读取当前计划任务快照。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_plan_get_tasks_", "clientVisible": false},
	}
}

func planUpdateTaskToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_plan_update_task_",
		Name:        "_plan_update_task_",
		Label:       "更新计划任务",
		Description: "更新计划中的任务状态。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"taskId": map[string]any{"type": "string"},
				"status": map[string]any{"type": "string"},
			},
			"required": []string{"taskId", "status"},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_plan_update_task_", "clientVisible": false},
	}
}

func bashToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_bash_",
		Name:        "_bash_",
		Label:       "执行命令（宿主机）",
		Description: "运行白名单 bash 命令（默认严格模式；可配置启用高级 shell 语法，如管道、重定向与 here-doc）",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_bash_"},
	}
}

func sandboxBashToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_sandbox_bash_",
		Name:        "_sandbox_bash_",
		Label:       "执行命令",
		Description: "在沙箱容器中执行命令。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string"},
				"cwd":        map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_sandbox_bash_"},
	}
}

func memorySearchToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "memory_search",
		Name:        "memory_search",
		Label:       "搜索记忆",
		Description: "搜索已存储的记忆摘要",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "memory_search"},
	}
}

func memoryReadToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "memory_read",
		Name:        "memory_read",
		Label:       "读取记忆",
		Description: "按 ID 读取单条记忆",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required": []string{"id"},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "memory_read"},
	}
}

func memoryWriteToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "memory_write",
		Name:        "memory_write",
		Label:       "写入记忆",
		Description: "写入一条记忆记录",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":         map[string]any{"type": "string"},
				"chatId":     map[string]any{"type": "string"},
				"subjectKey": map[string]any{"type": "string"},
				"summary":    map[string]any{"type": "string"},
				"sourceType": map[string]any{"type": "string"},
				"category":   map[string]any{"type": "string"},
				"importance": map[string]any{"type": "integer"},
			},
			"required": []string{"chatId", "summary"},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "memory_write"},
	}
}
