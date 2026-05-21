package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

var coderPlanningModePlanTools = []string{
	"file_read",
	"file_grep",
	"datetime",
	"ask_user_question",
	"planning_write",
}

const defaultCoderPlanningPrompt = `You are in CODER planning mode.

Planning rules:
1. You may inspect files and ask the user questions, but you must not execute or mutate anything.
2. Use ask_user_question whenever important intent, scope, or implementation choices are unclear.
3. Create a concrete execution plan with planning_write when you have enough information.
4. Do not claim execution has started. The backend will ask the user to confirm the plan before any execution tools are available.`

type coderPlanningStream struct {
	engine  *LLMAgentEngine
	ctx     context.Context
	req     api.QueryRequest
	session QuerySession
	execCtx *ExecutionContext

	settings PlanExecuteSettings
	pending  []AgentDelta
	current  AgentStream

	taskIndex           int
	planDone            bool
	executionDone       bool
	confirmationPending bool
	confirmationDone    bool
	summaryDone         bool
	completed           bool
	closed              bool
	taskLifecycle       bool

	executeMessages []openAIMessage
}

func newCoderPlanningStream(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	settings := resolvePlanExecuteRuntimeSettings(session, engine.cfg.Defaults.Plan.MaxSteps, engine.cfg.Defaults.Plan.MaxWorkRoundsPerTask)
	execCtx := &ExecutionContext{
		Request:       req,
		Session:       session,
		RunControl:    RunControlFromContext(ctx),
		Budget:        NormalizeBudget(session.ResolvedBudget),
		StageSettings: settings,
		RunLoopState:  RunLoopStateIdle,
	}
	return &coderPlanningStream{
		engine:   engine,
		ctx:      ctx,
		req:      req,
		session:  session,
		execCtx:  execCtx,
		settings: settings,
		pending: []AgentDelta{
			DeltaStageMarker{Stage: "coder-plan"},
		},
	}, nil
}

func (s *coderPlanningStream) Next() (AgentDelta, error) {
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			return event, nil
		}
		if s.completed {
			return nil, io.EOF
		}
		if s.confirmationPending {
			if err := s.awaitPlanConfirmation(); err != nil {
				return nil, err
			}
			continue
		}
		if s.current == nil {
			if err := s.advance(); err != nil {
				return nil, err
			}
			continue
		}
		event, err := s.current.Next()
		if err == io.EOF {
			if llmStream, ok := s.current.(*llmRunStream); ok {
				if !s.planDone || !s.executionDone {
					s.executeMessages = append(s.executeMessages, nonSystemMessages(llmStream.AccumulatedMessages())...)
				}
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

func (s *coderPlanningStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.current != nil {
		return s.current.Close()
	}
	return nil
}

func (s *coderPlanningStream) advance() error {
	if !s.planDone {
		return s.startPlanStage()
	}
	if !s.confirmationDone {
		return nil
	}
	if !s.executionDone {
		return s.startExecutionStage()
	}
	if !s.summaryDone {
		return s.startSummaryStage()
	}
	s.completed = true
	return nil
}

func (s *coderPlanningStream) startPlanStage() error {
	planPrompt := s.planningPrompt()
	req := s.req
	req.Message = strings.TrimSpace(planPrompt) + "\n\nUser request:\n" + s.req.Message
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Plan, s.planStageTools()), true, runStreamOptions{
		ExecCtx:             s.execCtx,
		ToolNames:           s.planStageTools(),
		ModelKey:            s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:            s.settings.MaxSteps,
		Stage:               "coder-plan",
		MaxToolCallsPerTurn: 1,
		PostToolHook:        s.planStagePostToolHook,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) planningPrompt() string {
	custom := strings.TrimSpace(s.settings.Plan.PrimaryPrompt())
	prompt := strings.TrimSpace(defaultCoderPlanningPrompt)
	if custom != "" {
		prompt = custom + "\n\n" + prompt
	}
	if desc := s.buildExecuteToolDescriptions(); desc != "" {
		prompt += "\n\n" + desc
	}
	return prompt + "\n\nCreate a standard Markdown execution plan for the user's request. You MUST call planning_write before the planning phase finishes. The plan must include Summary, Key Changes, Plan, Test Plan, and Assumptions. When calling planning_write, emit arguments in this field order: title, summary, keyChanges, steps, testPlan, assumptions."
}

func (s *coderPlanningStream) afterStageEOF() error {
	if !s.planDone {
		s.planDone = true
		if s.execCtx == nil || s.execCtx.PlanningState == nil || strings.TrimSpace(s.execCtx.PlanningState.Markdown) == "" {
			s.pending = append(s.pending, DeltaError{
				Error: NewErrorPayload(
					"plan_not_created",
					"CODER planning mode did not write a Markdown plan via planning_write",
					ErrorScopeRun,
					ErrorCategorySystem,
					nil,
				),
			})
			s.completed = true
			s.summaryDone = true
			return nil
		}
		s.emitPlanConfirmationAsk()
		return nil
	}

	if !s.executionDone {
		s.executionDone = true
		return nil
	}

	s.summaryDone = true
	s.completed = true
	return nil
}

func (s *coderPlanningStream) emitPlanConfirmationAsk() {
	awaitAsk := s.planConfirmationAsk()
	if s.execCtx != nil && s.execCtx.RunControl != nil {
		awaitingCtx := awaitingContextFromDeltaAsk(awaitAsk)
		awaitingCtx.NoTimeout = true
		s.execCtx.RunControl.ExpectSubmit(awaitingCtx)
	}
	s.pending = append(s.pending, awaitAsk)
	s.confirmationPending = true
}

func (s *coderPlanningStream) planConfirmationAsk() DeltaAwaitAsk {
	return DeltaAwaitAsk{
		AwaitingID:   s.session.RunID + "_coder_plan_confirm",
		Mode:         "approval",
		Timeout:      0,
		RunID:        s.session.RunID,
		ViewportType: "builtin",
		ViewportKey:  "approval",
		Approvals: []any{
			map[string]any{
				"id":            "confirm",
				"command":       "是否按此计划执行？",
				"description":   "确认后将按当前 Markdown plan 开始执行",
				"allowFreeText": false,
				"options": []any{
					map[string]any{"label": "执行计划", "decision": "approve", "description": "按当前计划开始执行任务"},
					map[string]any{"label": "取消执行", "decision": "reject", "description": "停止本轮，不执行计划任务"},
				},
			},
		},
	}
}

func (s *coderPlanningStream) awaitPlanConfirmation() error {
	s.confirmationPending = false
	s.confirmationDone = true
	awaitingID := s.session.RunID + "_coder_plan_confirm"
	if s.execCtx == nil || s.execCtx.RunControl == nil {
		return ErrRunControlUnavailable
	}
	defer s.execCtx.RunControl.ClearExpectedSubmit(awaitingID)

	s.execCtx.RunLoopState = RunLoopStateWaitingSubmit
	s.execCtx.RunControl.TransitionState(RunLoopStateWaitingSubmit)
	submitResult, err := s.execCtx.RunControl.AwaitSubmitWithTimeout(s.ctx, awaitingID, 0)
	if err != nil {
		if errors.Is(err, ErrRunInterrupted) {
			s.pending = append(s.pending, DeltaRunCancel{RunID: s.session.RunID})
			s.completed = true
			return nil
		}
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     AwaitingErrorAnswer("approval", "invalid_submit", err.Error()),
		})
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	s.execCtx.RunControl.TransitionState(RunLoopStateToolExecuting)
	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: awaitingID,
		Params:     submitResult.Request.Params,
	})

	args := s.planConfirmationArgs()
	normalized, normalizeErr := normalizeHITLApprovalSubmit(args, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     AwaitingErrorAnswer("approval", "invalid_submit", normalizeErr.Error()),
		})
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     CloneMap(normalized),
	})

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") || confirmationDecision(normalized) != "approve" {
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
	return nil
}

func (s *coderPlanningStream) planConfirmationArgs() map[string]any {
	ask := s.planConfirmationAsk()
	return map[string]any{
		"mode":      ask.Mode,
		"approvals": append([]any(nil), ask.Approvals...),
	}
}

func confirmationDecision(normalized map[string]any) string {
	switch approvals := normalized["approvals"].(type) {
	case []map[string]any:
		if len(approvals) == 0 {
			return ""
		}
		return strings.TrimSpace(AnyStringNode(approvals[0]["decision"]))
	case []any:
		if len(approvals) == 0 {
			return ""
		}
		approval, _ := approvals[0].(map[string]any)
		return strings.TrimSpace(AnyStringNode(approval["decision"]))
	default:
		return ""
	}
}

func (s *coderPlanningStream) cancelUnstartedPlan(message string) {
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		s.execCtx.PlanningState.Status = "canceled"
		s.pending = append(s.pending, DeltaPlanningEnd{
			PlanningID:   s.execCtx.PlanningState.PlanningID,
			PlanningFile: s.execCtx.PlanningState.PlanningFile,
			ChatID:       s.session.ChatID,
			RunID:        s.session.RunID,
			RequestID:    s.session.RequestID,
			AgentKey:     s.session.AgentKey,
			Title:        s.execCtx.PlanningState.Title,
			Status:       "canceled",
			Markdown:     s.execCtx.PlanningState.Markdown,
		})
	}
	if strings.TrimSpace(message) != "" {
		s.pending = append(s.pending, DeltaContent{Text: message})
	}
	s.summaryDone = true
	s.completed = true
}

func (s *coderPlanningStream) startExecutionStage() error {
	s.pending = append(s.pending, DeltaStageMarker{Stage: "coder-execute"})
	planningMarkdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningMarkdown = s.execCtx.PlanningState.Markdown
	}
	executePrompt := "Execute the confirmed CODER plan.\n\nOriginal request:\n" + s.req.Message + "\n\nConfirmed plan:\n" + planningMarkdown
	messages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Execute.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Execute the confirmed CODER plan for the user."
	}
	messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, openAIMessage{Role: "user", Content: executePrompt})

	req := s.req
	req.Message = executePrompt
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:   s.execCtx,
		Messages:  messages,
		ToolNames: s.executeStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Execute),
		Stage:     "coder-execute",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) advanceTaskExecution() error {
	task := &s.execCtx.PlanState.Tasks[s.taskIndex]
	if task.Status == "" || task.Status == "init" {
		task.Status = "in_progress"
	}
	s.execCtx.PlanState.ActiveTaskID = task.TaskID
	s.pending = append(s.pending,
		DeltaStageMarker{Stage: fmt.Sprintf("coder-execute-task-%d", s.taskIndex+1)},
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

func (s *coderPlanningStream) startTaskStream(task *PlanTask) error {
	beforeStatus := NormalizePlanTaskStatus(task.Status)
	taskPrompt := renderTemplate(defaultTaskTemplate(s.settings), map[string]string{
		"task_list":        formatTaskList(s.execCtx.PlanState.Tasks),
		"task_id":          task.TaskID,
		"task_description": task.Description,
	})
	messages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Execute.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Execute the current confirmed CODER plan task."
	}
	messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, openAIMessage{Role: "user", Content: taskPrompt})

	req := s.req
	req.Message = taskPrompt
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:             s.execCtx,
		Messages:            messages,
		ToolNames:           s.executeStageTools(),
		ModelKey:            s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:            s.settings.MaxWorkRoundsPerTask,
		Stage:               fmt.Sprintf("coder-execute-step-%d", s.taskIndex+1),
		MaxToolCallsPerTurn: 1,
		PostToolHook: func(toolName string, _ string) PostToolHookResult {
			if !isPlanTool(toolName) {
				return PostToolContinue
			}
			afterStatus := NormalizePlanTaskStatus(task.Status)
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

func (s *coderPlanningStream) startSummaryStage() error {
	s.pending = append(s.pending, DeltaStageMarker{Stage: "coder-summary"})
	summaryMessages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Summary.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Summarize the completed confirmed CODER plan execution for the user."
	}
	summaryMessages = append(summaryMessages, openAIMessage{Role: "system", Content: systemPrompt})
	summaryMessages = append(summaryMessages, s.executeMessages...)
	planningMarkdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningMarkdown = s.execCtx.PlanningState.Markdown
	}
	summaryMessages = append(summaryMessages, openAIMessage{
		Role:    "user",
		Content: "Please provide a final summary of the completed confirmed plan.\n\nOriginal request:\n" + s.req.Message + "\n\nConfirmed plan:\n" + planningMarkdown,
	})

	stream, err := s.engine.newRunStreamWithOptions(s.ctx, s.req, s.sessionForStage(s.settings.Summary, nil), false, runStreamOptions{
		ExecCtx:   s.execCtx,
		Messages:  summaryMessages,
		ToolNames: nil,
		ModelKey:  s.resolveStageModelKey(s.settings.Summary),
		MaxSteps:  1,
		Stage:     "coder-summary",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) emitTaskTerminal(task *PlanTask, status string) {
	switch status {
	case "completed":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID, Status: status})
	case "canceled":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID, Status: status})
	case "failed":
		s.pending = append(s.pending, DeltaTaskLifecycle{
			Kind:   "fail",
			TaskID: task.TaskID,
			Status: status,
			Error: NewErrorPayload("task_failed", "Task status updated to failed",
				ErrorScopeTask, ErrorCategorySystem, nil),
		})
	}
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *coderPlanningStream) emitTaskFailure(task *PlanTask, message string) {
	task.Status = "failed"
	s.pending = append(s.pending, DeltaPlanUpdate{
		PlanID: s.execCtx.PlanState.PlanID,
		ChatID: s.session.ChatID,
		Plan:   PlanTasksArray(s.execCtx.PlanState),
	})
	s.pending = append(s.pending, DeltaTaskLifecycle{
		Kind:   "fail",
		TaskID: task.TaskID,
		Status: "failed",
		Error: NewErrorPayload("task_execution_error", message,
			ErrorScopeTask, ErrorCategorySystem, map[string]any{"taskId": task.TaskID}),
	})
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *coderPlanningStream) planStageTools() []string {
	return append([]string(nil), coderPlanningModePlanTools...)
}

func (s *coderPlanningStream) executeStageTools() []string {
	tools := stageToolsOrDefault(s.settings.Execute, s.session.ToolNames)
	return removeToolNames(tools, "plan_add_tasks", "plan_get_tasks", "plan_update_task", "planning_write", "ask_user_question")
}

func isPlanningOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "plan_add_tasks", "plan_get_tasks", "plan_update_task", "planning_write", "ask_user_question":
		return true
	default:
		return false
	}
}

func (s *coderPlanningStream) planStagePostToolHook(toolName string, _ string) PostToolHookResult {
	if !isPlanningWriteTool(toolName) {
		return PostToolContinue
	}
	if s.execCtx != nil && s.execCtx.PlanningState != nil && strings.TrimSpace(s.execCtx.PlanningState.Markdown) != "" {
		return PostToolStop
	}
	return PostToolContinue
}

func (s *coderPlanningStream) buildExecuteToolDescriptions() string {
	tools := s.executeStageTools()
	if len(tools) == 0 {
		return ""
	}
	descByName := s.toolDescriptionsByName()
	var lines []string
	for _, toolName := range tools {
		if isPlanningOnlyTool(toolName) {
			continue
		}
		desc := strings.TrimSpace(descByName[strings.ToLower(strings.TrimSpace(toolName))])
		if desc == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", toolName, desc))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Tools available only after the user confirms the plan:\n" + strings.Join(lines, "\n")
}

func (s *coderPlanningStream) toolDescriptionsByName() map[string]string {
	defs := s.engine.tools.Definitions()
	out := make(map[string]string, len(defs))
	for _, def := range defs {
		name := strings.ToLower(strings.TrimSpace(def.Name))
		if name == "" {
			continue
		}
		out[name] = def.Description
	}
	return out
}

func (s *coderPlanningStream) sessionForStage(stage StageSettings, toolNames []string) QuerySession {
	session := s.session
	if modelKey := s.resolveStageModelKey(stage); modelKey != "" {
		session.ModelKey = modelKey
	}
	if toolNames != nil {
		session.ToolNames = append([]string(nil), toolNames...)
	}
	return session
}

func (s *coderPlanningStream) resolveStageModelKey(stage StageSettings) string {
	if strings.TrimSpace(stage.ModelKey) != "" {
		return strings.TrimSpace(stage.ModelKey)
	}
	return s.session.ModelKey
}

func removeToolNames(base []string, names ...string) []string {
	blocked := map[string]struct{}{}
	for _, name := range names {
		if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
			blocked[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(base))
	for _, name := range base {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		out = append(out, name)
	}
	return out
}
