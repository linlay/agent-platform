package engine

import (
	"context"
	"fmt"
	"io"
	"strings"

	"agent-platform-runner-go/internal/api"
)

const defaultTaskExecutionPromptTemplate = `Task list:
{{task_list}}
Current task ID: {{task_id}}
Current task description: {{task_description}}
Execution rules:
1) Call at most one tool per round.
2) You may call any available tool as needed.
3) Before finishing this task, you MUST call _plan_update_task_ to update its status.`

type planExecuteStream struct {
	engine  *LLMAgentEngine
	ctx     context.Context
	req     api.QueryRequest
	session QuerySession
	execCtx *ExecutionContext

	settings      PlanExecuteSettings
	pending       []AgentDelta
	current       AgentStream
	taskIndex     int
	planDone      bool
	summaryDone   bool
	completed     bool
	closed        bool
	taskLifecycle bool
}

func newPlanExecuteStream(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	execCtx := &ExecutionContext{
		Request:       req,
		Session:       session,
		RunControl:    RunControlFromContext(ctx),
		Budget:        normalizeBudget(session.ResolvedBudget),
		StageSettings: session.ResolvedStageSettings,
		RunLoopState:  RunLoopStateIdle,
		PlanState: &PlanRuntimeState{
			PlanID: session.RunID + "_plan",
		},
	}
	stream := &planExecuteStream{
		engine:   engine,
		ctx:      ctx,
		req:      req,
		session:  session,
		execCtx:  execCtx,
		settings: session.ResolvedStageSettings,
		pending: []AgentDelta{
			DeltaStageMarker{Stage: "plan"},
			DeltaPlanUpdate{
				PlanID: session.RunID + "_plan",
				ChatID: session.ChatID,
				Plan:   planStatePayload(execCtx.PlanState),
			},
		},
	}
	if stream.settings.MaxSteps <= 0 || stream.settings.MaxWorkRoundsPerTask <= 0 {
		stream.settings = ResolvePlanExecuteSettings(session.StageSettings, engine.cfg.Defaults.Plan.MaxSteps, engine.cfg.Defaults.Plan.MaxWorkRoundsPerTask)
	}
	return stream, nil
}

func (s *planExecuteStream) Next() (AgentDelta, error) {
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			return event, nil
		}
		if s.completed {
			return nil, io.EOF
		}
		if s.current == nil {
			if err := s.advance(); err != nil {
				return nil, err
			}
			continue
		}
		event, err := s.current.Next()
		if err == io.EOF {
			_ = s.current.Close()
			s.current = nil
			if err := s.afterStageEOF(); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		return event, nil
	}
}

func (s *planExecuteStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.current != nil {
		return s.current.Close()
	}
	return nil
}

func (s *planExecuteStream) advance() error {
	if !s.planDone {
		return s.startPlanStage()
	}
	if s.taskIndex < len(s.execCtx.PlanState.Tasks) {
		return s.startNextTask()
	}
	if !s.summaryDone {
		return s.startSummaryStage()
	}
	s.completed = true
	return nil
}

func (s *planExecuteStream) afterStageEOF() error {
	if !s.planDone {
		s.planDone = true
		if len(s.execCtx.PlanState.Tasks) == 0 {
			s.pending = append(s.pending, DeltaError{
				Error: NewErrorPayload(
					"plan_not_created",
					"planning stage did not create any tasks via _plan_add_tasks_",
					ErrorScopeRun,
					ErrorCategorySystem,
					nil,
				),
			})
			s.summaryDone = true
			s.completed = true
			return nil
		}
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   planStatePayload(s.execCtx.PlanState),
		})
		return nil
	}
	if s.taskLifecycle {
		s.taskLifecycle = false
		task := s.execCtx.PlanState.Tasks[s.taskIndex]
		finalStatus := normalizePlanTaskStatus(task.Status)
		if finalStatus == "" || finalStatus == "init" || finalStatus == "in_progress" {
			finalStatus = "failed"
			s.execCtx.PlanState.Tasks[s.taskIndex].Status = finalStatus
		}
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   planStatePayload(s.execCtx.PlanState),
		})
		switch finalStatus {
		case "completed":
			s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID})
		case "canceled":
			s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID})
		default:
			s.pending = append(s.pending, DeltaTaskLifecycle{
				Kind:   "fail",
				TaskID: task.TaskID,
				Error: NewErrorPayload(
					"task_execution_error",
					"task execution did not reach a terminal _plan_update_task_ status",
					ErrorScopeTask,
					ErrorCategorySystem,
					map[string]any{
						"taskId": task.TaskID,
						"status": finalStatus,
					},
				),
			})
		}
		s.taskIndex++
		return nil
	}
	s.summaryDone = true
	s.completed = true
	return nil
}

func (s *planExecuteStream) startPlanStage() error {
	req := s.req
	req.Message = "Create an execution plan for the user's request. You MUST call _plan_add_tasks_ before the stage finishes.\n\nUser request:\n" + s.req.Message
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Plan, s.planStageTools()), true, runStreamOptions{
		ExecCtx:   s.execCtx,
		ToolNames: s.planStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:  minPositive(s.settings.MaxSteps, 6),
		Stage:     "plan",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *planExecuteStream) startNextTask() error {
	task := &s.execCtx.PlanState.Tasks[s.taskIndex]
	if task.Status == "" || task.Status == "init" {
		task.Status = "in_progress"
	}
	s.execCtx.PlanState.ActiveTaskID = task.TaskID
	s.pending = append(s.pending,
		DeltaStageMarker{Stage: "execute"},
		DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   planStatePayload(s.execCtx.PlanState),
		},
		DeltaTaskLifecycle{
			Kind:        "start",
			TaskID:      task.TaskID,
			RunID:       s.session.RunID,
			TaskName:    task.TaskID,
			Description: task.Description,
		},
	)
	req := s.req
	req.Message = renderTemplate(defaultTaskTemplate(s.settings), map[string]string{
		"task_list":        formatTaskList(s.execCtx.PlanState.Tasks),
		"task_id":          task.TaskID,
		"task_description": task.Description,
	})
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:   s.execCtx,
		ToolNames: s.executeStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:  s.settings.MaxWorkRoundsPerTask,
		Stage:     "execute",
	})
	if err != nil {
		return err
	}
	s.current = stream
	s.taskLifecycle = true
	return nil
}

func (s *planExecuteStream) startSummaryStage() error {
	s.pending = append(s.pending, DeltaStageMarker{Stage: "summary"})
	req := s.req
	req.Message = "Summarize the completed plan execution for the user.\n\nOriginal request:\n" + s.req.Message + "\n\nTask results:\n" + formatTaskList(s.execCtx.PlanState.Tasks)
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Summary, nil), false, runStreamOptions{
		ExecCtx:   s.execCtx,
		ToolNames: nil,
		ModelKey:  s.resolveStageModelKey(s.settings.Summary),
		MaxSteps:  1,
		Stage:     "summary",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *planExecuteStream) sessionForStage(stage StageSettings, toolNames []string) QuerySession {
	session := s.session
	if modelKey := s.resolveStageModelKey(stage); modelKey != "" {
		session.ModelKey = modelKey
	}
	if toolNames != nil {
		session.ToolNames = append([]string(nil), toolNames...)
	}
	return session
}

func (s *planExecuteStream) resolveStageModelKey(stage StageSettings) string {
	if strings.TrimSpace(stage.ModelKey) != "" {
		return strings.TrimSpace(stage.ModelKey)
	}
	return s.session.ModelKey
}

func (s *planExecuteStream) planStageTools() []string {
	tools := stageToolsOrDefault(s.settings.Plan, s.session.ToolNames)
	return appendUniqueTools(tools, "_plan_add_tasks_", "_plan_get_tasks_", "_plan_update_task_")
}

func (s *planExecuteStream) executeStageTools() []string {
	tools := stageToolsOrDefault(s.settings.Execute, s.session.ToolNames)
	return appendUniqueTools(tools, "_plan_get_tasks_", "_plan_update_task_")
}

func stageToolsOrDefault(stage StageSettings, fallback []string) []string {
	if len(stage.Tools) > 0 {
		return append([]string(nil), stage.Tools...)
	}
	return append([]string(nil), fallback...)
}

func appendUniqueTools(base []string, extra ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, item := range append(base, extra...) {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func defaultTaskTemplate(settings PlanExecuteSettings) string {
	if strings.TrimSpace(settings.TaskExecutionPrompt) != "" {
		return settings.TaskExecutionPrompt
	}
	return defaultTaskExecutionPromptTemplate
}

func renderTemplate(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
		result = strings.ReplaceAll(result, "{{ "+key+" }}", value)
	}
	return result
}

func formatTaskList(tasks []PlanTask) string {
	if len(tasks) == 0 {
		return "- (empty)"
	}
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", task.TaskID, task.Status, task.Description))
	}
	return strings.Join(lines, "\n")
}

func minPositive(value int, fallback int) int {
	if value > 0 && value < fallback {
		return value
	}
	if fallback > 0 {
		return fallback
	}
	return value
}
