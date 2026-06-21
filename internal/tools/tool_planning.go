package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func (t *RuntimeToolExecutor) invokePlanningWrite(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = FinalizePlanningToolName
	}
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "planning_context_unavailable", ExitCode: -1}, nil
	}
	if !execCtx.Session.PlanningMode {
		return ToolExecutionResult{Output: "失败: " + toolName + " 只能在 CODER planningMode 阶段使用", Error: planningToolErrorCode(toolName, "not_allowed"), ExitCode: -1}, nil
	}
	if execCtx.PlanningState != nil && strings.TrimSpace(execCtx.PlanningState.Markdown) != "" {
		return ToolExecutionResult{Output: "失败: " + toolName + " 已经写入过规划", Error: planningToolErrorCode(toolName, "already_exists"), ExitCode: -1}, nil
	}
	chatsDir := strings.TrimSpace(t.cfg.Paths.ChatsDir)
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	if chatsDir == "" {
		return ToolExecutionResult{Output: "失败: CHATS_DIR 未配置", Error: "chats_dir_unavailable", ExitCode: -1}, nil
	}

	spec := planutil.SpecFromArgs(args)
	markdown := planutil.RenderMarkdown(spec)
	if !planutil.ValidMarkdown(markdown) {
		return ToolExecutionResult{Output: "失败: markdown 不能为空", Error: "missing_markdown", ExitCode: -1}, nil
	}

	revision := planningRevision(execCtx)
	execCtx.PlanningRevision = revision
	planningID := planutil.PlanningIDForRevision(planningRunID(execCtx), revision)
	planningFile := planutil.PlanningFileForChat(chatsDir, execCtx.Session.ChatID, planningID)
	if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
		return ToolExecutionResult{Output: "失败: 创建 planning 目录失败: " + err.Error(), Error: planningToolErrorCode(toolName, "failed"), ExitCode: -1}, nil
	}
	if err := os.WriteFile(planningFile, []byte(markdown), 0o644); err != nil {
		return ToolExecutionResult{Output: "失败: 写入 planning markdown 失败: " + err.Error(), Error: planningToolErrorCode(toolName, "failed"), ExitCode: -1}, nil
	}

	execCtx.PlanningState = &PlanningRuntimeState{
		PlanningID:   planningID,
		PlanningFile: planningFile,
		Markdown:     markdown,
		ToolName:     toolName,
	}
	payload := map[string]any{
		"planningId":   planningID,
		"planningFile": planningFile,
		"markdown":     markdown,
	}
	result := structuredResultWithExit(payload, 0)
	result.Output = fmt.Sprintf("planning written: %s", planningFile)
	return result, nil
}

func planningToolErrorCode(toolName string, suffix string) string {
	return FinalizePlanningToolName + "_" + strings.TrimSpace(suffix)
}

func planningRunID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Session.RequestID)
	}
	return runID
}

func planningRevision(execCtx *ExecutionContext) int {
	if execCtx == nil || execCtx.PlanningRevision <= 0 {
		return 1
	}
	return execCtx.PlanningRevision
}
