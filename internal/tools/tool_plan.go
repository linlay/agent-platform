package tools

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) invokePlanAddTasks(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	mode := normalizePlanAddTasksMode(AnyStringNode(args["mode"]))
	if mode == "" {
		return ToolExecutionResult{Output: "失败: 非法 mode，仅支持 append/new", Error: "invalid_plan_mode", ExitCode: -1}, nil
	}
	var tasks []PlanTask
	if rawTasks, ok := args["tasks"].([]any); ok {
		for _, item := range rawTasks {
			taskMap, _ := item.(map[string]any)
			description := AnyStringNode(taskMap["description"])
			if strings.TrimSpace(description) == "" {
				continue
			}
			taskID := AnyStringNode(taskMap["taskId"])
			if strings.TrimSpace(taskID) == "" {
				taskID = shortPlanID()
			}
			tasks = append(tasks, PlanTask{
				TaskID:      taskID,
				Description: strings.TrimSpace(description),
				Status:      NormalizePlanTaskStatus(AnyStringNode(taskMap["status"])),
			})
		}
	}
	if len(tasks) == 0 {
		description := AnyStringNode(args["description"])
		if strings.TrimSpace(description) == "" {
			return ToolExecutionResult{Output: "失败: 缺少任务描述", Error: "missing_task_description", ExitCode: -1}, nil
		}
		taskID := AnyStringNode(args["taskId"])
		if strings.TrimSpace(taskID) == "" {
			taskID = shortPlanID()
		}
		tasks = append(tasks, PlanTask{
			TaskID:      taskID,
			Description: strings.TrimSpace(description),
			Status:      NormalizePlanTaskStatus(AnyStringNode(args["status"])),
		})
	}
	state := ensurePlanState(execCtx)
	if mode == "new" {
		state = &PlanRuntimeState{PlanID: newPlanID(execCtx.Session.RunID)}
		execCtx.PlanState = state
	}
	if state.PlanID == "" {
		state.PlanID = execCtx.Session.RunID + "_plan"
	}
	state.Tasks = append(state.Tasks, tasks...)
	t.persistPlanTasksSnapshot(execCtx, state)
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
	if execCtx == nil {
		payload := NewErrorPayload("plan_context_unavailable", "Plan context is unavailable in direct invocation", ErrorScopeRun, ErrorCategorySystem, nil)
		return ToolExecutionResult{Output: MarshalJSON(payload), Structured: payload, Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	return structuredResult(planStatePayload(t.ensureRestoredPlanState(execCtx))), nil
}

func (t *RuntimeToolExecutor) invokePlanUpdateTask(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "plan_context_unavailable", ExitCode: -1}, nil
	}
	state := t.ensureRestoredPlanState(execCtx)
	taskID := AnyStringNode(args["taskId"])
	if strings.TrimSpace(taskID) == "" {
		return ToolExecutionResult{Output: "失败: 缺少 taskId", Error: "missing_task_id", ExitCode: -1}, nil
	}
	status := NormalizePlanTaskStatus(AnyStringNode(args["status"]))
	if status == "" {
		return ToolExecutionResult{Output: "失败: 非法状态，仅支持 init/in_progress/completed/failed/canceled", Error: "invalid_task_status", ExitCode: -1}, nil
	}
	for index := range state.Tasks {
		storedTaskID := strings.TrimSpace(state.Tasks[index].TaskID)
		if storedTaskID != strings.TrimSpace(taskID) {
			continue
		}
		state.Tasks[index].Status = status
		if description := strings.TrimSpace(AnyStringNode(args["description"])); description != "" {
			state.Tasks[index].Description = description
		}
		if status == "in_progress" {
			state.ActiveTaskID = storedTaskID
		}
		if strings.TrimSpace(state.ActiveTaskID) == storedTaskID && (status == "completed" || status == "failed" || status == "canceled") {
			state.ActiveTaskID = ""
		}
		t.persistPlanTasksSnapshot(execCtx, state)
		return ToolExecutionResult{Output: "OK", Structured: planStatePayload(state), ExitCode: 0}, nil
	}
	return ToolExecutionResult{Output: "失败: taskId 不存在", Error: "task_not_found", ExitCode: -1}, nil
}

func ensurePlanState(execCtx *ExecutionContext) *PlanRuntimeState {
	if execCtx.PlanState == nil {
		execCtx.PlanState = &PlanRuntimeState{
			PlanID: execCtx.Session.RunID + "_plan",
		}
	}
	return execCtx.PlanState
}

func planStatePayload(state *PlanRuntimeState) map[string]any {
	if state == nil {
		return map[string]any{
			"plan": []map[string]any{},
		}
	}
	payload := map[string]any{
		"planId": state.PlanID,
		"plan":   PlanTasksArray(state),
	}
	if state.ActiveTaskID != "" {
		payload["currentTaskId"] = state.ActiveTaskID
	}
	return payload
}

var planTaskCounter atomic.Int64
var planIDCounter atomic.Int64

func normalizePlanAddTasksMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "append":
		return "append"
	case "new":
		return "new"
	default:
		return ""
	}
}

func newPlanID(runID string) string {
	base := strings.TrimSpace(runID)
	if base == "" {
		base = "run"
	}
	seq := planIDCounter.Add(1)
	return fmt.Sprintf("%s_plan_%d_%d", base, time.Now().UnixMilli(), seq)
}

func shortPlanID() string {
	seq := planTaskCounter.Add(1)
	return fmt.Sprintf("task_%d_%d", time.Now().UnixMilli(), seq)
}
