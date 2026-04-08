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

	executeMessages []openAIMessage // accumulated messages across rounds for summary
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
				Plan:   planTasksArray(execCtx.PlanState),
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
			// Capture accumulated messages before closing (for summary stage context)
			if llmStream, ok := s.current.(*llmRunStream); ok && s.taskLifecycle {
				s.executeMessages = append(s.executeMessages, llmStream.AccumulatedMessages()...)
			}
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
		return s.advanceTaskExecution()
	}
	if !s.summaryDone {
		return s.startSummaryStage()
	}
	s.completed = true
	return nil
}

// advanceTaskExecution starts execution for the current task.
// The llmRunStream handles the multi-turn tool execution loop internally.
// MaxToolCallsPerTurn=1 ensures only one tool is processed per round (Java behaviour).
// After the stream ends, afterStageEOF checks task status.
func (s *planExecuteStream) advanceTaskExecution() error {
	task := &s.execCtx.PlanState.Tasks[s.taskIndex]

	if task.Status == "" || task.Status == "init" {
		task.Status = "in_progress"
	}
	s.execCtx.PlanState.ActiveTaskID = task.TaskID
	s.pending = append(s.pending,
		DeltaStageMarker{Stage: fmt.Sprintf("execute-task-%d", s.taskIndex+1)},
		DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   planTasksArray(s.execCtx.PlanState),
		},
		DeltaTaskLifecycle{
			Kind:        "start",
			TaskID:      task.TaskID,
			RunID:       s.session.RunID,
			TaskName:    task.TaskID,
			Description: task.Description,
		},
	)

	return s.startTaskStream(task)
}

func (s *planExecuteStream) startTaskStream(task *PlanTask) error {
	beforeStatus := normalizePlanTaskStatus(task.Status)

	// Build messages from accumulated executeMessages (Java: context.executeMessages() is shared
	// across all tasks, so each task sees previous tasks' conversation + steers).
	taskPrompt := renderTemplate(defaultTaskTemplate(s.settings), map[string]string{
		"task_list":        formatTaskList(s.execCtx.PlanState.Tasks),
		"task_id":          task.TaskID,
		"task_description": task.Description,
	})
	messages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Execute.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Execute the current task."
	}
	messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, openAIMessage{Role: "user", Content: taskPrompt})

	req := s.req
	req.Message = taskPrompt
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:             s.execCtx,
		Messages:            messages, // Carry full execute history including steers
		ToolNames:           s.executeStageTools(),
		ModelKey:            s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:            s.settings.MaxWorkRoundsPerTask,
		SystemPrompt:        s.settings.Execute.PrimaryPrompt(),
		Stage:               fmt.Sprintf("execute-step-%d", s.taskIndex+1),
		MaxToolCallsPerTurn: 1,
		PostToolHook: func(toolName string, toolID string) PostToolHookResult {
			if !isPlanTool(toolName) {
				return PostToolContinue
			}
			afterStatus := normalizePlanTaskStatus(task.Status)
			if afterStatus != beforeStatus && isTerminalPlanStatus(afterStatus) {
				return PostToolStop
			}
			return PostToolContinue
		},
	})
	if err != nil {
		return err
	}
	s.current = stream
	s.taskLifecycle = true
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
			Plan:   planTasksArray(s.execCtx.PlanState),
		})
		return nil
	}

	if s.taskLifecycle {
		s.taskLifecycle = false
		task := &s.execCtx.PlanState.Tasks[s.taskIndex]
		finalStatus := normalizePlanTaskStatus(task.Status)

		if isTerminalPlanStatus(finalStatus) {
			s.emitTaskTerminal(task, finalStatus)
			if finalStatus == "failed" {
				// Failed tasks stop the entire plan (Java behaviour)
				s.taskIndex = len(s.execCtx.PlanState.Tasks)
			}
			return nil
		}

		// Task did not reach terminal status — mark as failed
		s.emitTaskFailure(task, "task execution did not reach a terminal status")
		return nil
	}

	// Summary stage finished
	s.summaryDone = true
	s.completed = true
	return nil
}

func (s *planExecuteStream) emitTaskTerminal(task *PlanTask, status string) {
	s.pending = append(s.pending, DeltaPlanUpdate{
		PlanID: s.execCtx.PlanState.PlanID,
		ChatID: s.session.ChatID,
		Plan:   planTasksArray(s.execCtx.PlanState),
	})
	switch status {
	case "completed":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID})
	case "canceled":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID})
	case "failed":
		s.pending = append(s.pending, DeltaTaskLifecycle{
			Kind:   "fail",
			TaskID: task.TaskID,
			Error: NewErrorPayload("task_failed", "Task status updated to failed",
				ErrorScopeTask, ErrorCategorySystem, nil),
		})
	}
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *planExecuteStream) emitTaskFailure(task *PlanTask, message string) {
	task.Status = "failed"
	s.pending = append(s.pending, DeltaPlanUpdate{
		PlanID: s.execCtx.PlanState.PlanID,
		ChatID: s.session.ChatID,
		Plan:   planTasksArray(s.execCtx.PlanState),
	})
	s.pending = append(s.pending, DeltaTaskLifecycle{
		Kind:   "fail",
		TaskID: task.TaskID,
		Error: NewErrorPayload("task_execution_error", message,
			ErrorScopeTask, ErrorCategorySystem, map[string]any{"taskId": task.TaskID}),
	})
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *planExecuteStream) startPlanStage() error {
	// Augment plan prompt with execute-stage tool descriptions (Java: augmentPlanStageWithToolPrompts)
	planPrompt := s.settings.Plan.PrimaryPrompt()
	executeToolDesc := s.buildExecuteToolDescriptions()
	if executeToolDesc != "" {
		planPrompt = planPrompt + "\n\n" + executeToolDesc
	}
	planCallableDesc := s.buildPlanCallableToolDescriptions()
	if planCallableDesc != "" {
		planPrompt = planPrompt + "\n\n" + planCallableDesc
	}

	req := s.req
	req.Message = strings.TrimSpace(planPrompt) + "\n\nCreate an execution plan for the user's request. You MUST call _plan_add_tasks_ before the stage finishes.\n\nUser request:\n" + s.req.Message
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Plan, s.planStageTools()), true, runStreamOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:     minPositive(s.settings.MaxSteps, 6),
		SystemPrompt: planPrompt,
		Stage:        "plan",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *planExecuteStream) startSummaryStage() error {
	s.pending = append(s.pending, DeltaStageMarker{Stage: "summary"})

	// Build summary messages from accumulated execute history (Java: context.executeMessages())
	summaryMessages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Summary.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Summarize the completed plan execution for the user."
	}
	summaryMessages = append(summaryMessages, openAIMessage{Role: "system", Content: systemPrompt})
	summaryMessages = append(summaryMessages, s.executeMessages...)
	summaryMessages = append(summaryMessages, openAIMessage{
		Role:    "user",
		Content: "Please provide a final summary of the completed plan.\n\nOriginal request:\n" + s.req.Message + "\n\nTask results:\n" + formatTaskList(s.execCtx.PlanState.Tasks),
	})

	stream, err := s.engine.newRunStreamWithOptions(s.ctx, s.req, s.sessionForStage(s.settings.Summary, nil), false, runStreamOptions{
		ExecCtx:      s.execCtx,
		Messages:     summaryMessages,
		ToolNames:    nil,
		ModelKey:     s.resolveStageModelKey(s.settings.Summary),
		MaxSteps:     1,
		SystemPrompt: s.settings.Summary.PrimaryPrompt(),
		Stage:        "summary",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

// buildExecuteToolDescriptions returns a prompt section describing execute-stage
// tools for reference during planning (Java: augmentPlanStageWithToolPrompts).
func (s *planExecuteStream) buildExecuteToolDescriptions() string {
	tools := s.executeStageTools()
	if len(tools) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "以下是执行阶段可用工具说明（当前是规划阶段，仅供参考，不允许调用）:")
	for _, toolName := range tools {
		if strings.HasPrefix(toolName, "_plan_") {
			continue // skip plan tools in reference section
		}
		lines = append(lines, fmt.Sprintf("- %s", toolName))
	}
	if len(lines) <= 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (s *planExecuteStream) buildPlanCallableToolDescriptions() string {
	return "当前规划阶段可调用工具（必须调用 _plan_add_tasks_ 创建计划）:\n- _plan_add_tasks_"
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
	// Only _plan_add_tasks_ is callable in plan stage (Java: selectPlanCallableTools)
	return appendUniqueTools(tools, "_plan_add_tasks_")
}

func (s *planExecuteStream) executeStageTools() []string {
	tools := stageToolsOrDefault(s.settings.Execute, s.session.ToolNames)
	// _plan_update_task_ for status updates, no _plan_get_tasks_ (per Zhang Qian's feedback)
	return appendUniqueTools(tools, "_plan_update_task_")
}

func isTerminalPlanStatus(status string) bool {
	switch status {
	case "completed", "canceled", "failed":
		return true
	default:
		return false
	}
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
