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
	"file_glob",
	"file_grep",
	"datetime",
	"regex",
	"vision_recognize",
	"ask_user_question",
	FinalizePlanningToolName,
}

const defaultCoderSummarySystemPrompt = `Summarize the completed confirmed CODER plan execution for the user.`

const defaultCoderExecuteSystemPrompt = `Execute the confirmed CODER plan for the user.`

const defaultCoderSummaryUserPromptTemplate = `Please provide a final summary of the completed confirmed plan.

Original request:
{{original_request}}

Confirmed plan:
{{confirmed_plan}}`

type coderPlanningStream struct {
	engine  *LLMAgentEngine
	ctx     context.Context
	req     api.QueryRequest
	session QuerySession
	execCtx *ExecutionContext

	settings PlanExecuteSettings
	pending  []AgentDelta
	current  AgentStream

	taskIndex             int
	planDone              bool
	executionDone         bool
	confirmationPending   bool
	confirmationDone      bool
	summaryDone           bool
	completed             bool
	closed                bool
	taskLifecycle         bool
	nextPlanIsFeedback    bool
	currentPlanIsFeedback bool

	rejectedPlanMarkdown string
	rejectedPlanDecision string
	rejectedPlanReason   string

	executeMessages     []openAIMessage
	summaryBaseMessages []openAIMessage
}

func newCoderPlanningStream(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	settings := resolvePlanExecuteRuntimeSettings(session, engine.cfg.Defaults.Plan.MaxSteps, engine.cfg.Defaults.Plan.MaxWorkRoundsPerTask)
	execCtx := &ExecutionContext{
		Request:          req,
		Session:          session,
		RunControl:       RunControlFromContext(ctx),
		Budget:           NormalizeBudget(session.ResolvedBudget),
		StageSettings:    settings,
		RunLoopState:     RunLoopStateIdle,
		PlanningRevision: 1,
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
				accumulated := llmStream.AccumulatedMessages()
				if !s.planDone || !s.executionDone {
					if s.currentPlanIsFeedback {
						s.executeMessages = nonSystemMessages(accumulated)
					} else {
						s.executeMessages = append(s.executeMessages, nonSystemMessages(accumulated)...)
					}
				}
				if s.planDone && !s.executionDone {
					s.summaryBaseMessages = append([]openAIMessage(nil), accumulated...)
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
		if s.nextPlanIsFeedback {
			return s.startPlanningFeedbackStage()
		}
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
	s.currentPlanIsFeedback = false
	planPrompt := s.planningPrompt()
	req := s.req
	req.Message = strings.TrimSpace(planPrompt) + "\n\nUser request:\n" + s.req.Message
	stageSession := s.sessionForStage(s.settings.Plan, s.planStageTools())
	stageSession.CurrentMessages = s.engine.buildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, stageSession, true, runStreamOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:     s.settings.MaxSteps,
		Stage:        "coder-plan",
		PostToolHook: s.planStagePostToolHook,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) startPlanningFeedbackStage() error {
	s.nextPlanIsFeedback = false
	s.currentPlanIsFeedback = true
	s.pending = append(s.pending, DeltaStageMarker{Stage: "coder-plan-feedback"})
	req := s.req
	req.Message = s.planningFeedbackPrompt()
	stageSession := s.sessionForStage(s.settings.Plan, s.planStageTools())
	stageSession.HistoryMessages = append(stageSession.HistoryMessages, rawMessagesFromOpenAIMessages(s.executeMessages)...)
	stageSession.CurrentMessages = s.engine.buildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, stageSession, true, runStreamOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:     s.settings.MaxSteps,
		Stage:        "coder-plan-feedback",
		PostToolHook: s.planStagePostToolHook,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func rawMessagesFromOpenAIMessages(messages []openAIMessage) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		raw := rawMessageFromOpenAIMessage(message)
		if len(raw) > 0 {
			out = append(out, raw)
		}
	}
	return out
}

func rawMessageFromOpenAIMessage(message openAIMessage) map[string]any {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		return nil
	}
	raw := map[string]any{"role": role}
	if content, ok := message.Content.(string); ok && strings.TrimSpace(content) != "" {
		raw["content"] = content
	} else if message.Content != nil {
		raw["content"] = message.Content
	}
	if strings.TrimSpace(message.Name) != "" {
		raw["name"] = strings.TrimSpace(message.Name)
	}
	if strings.TrimSpace(message.ToolCallID) != "" {
		raw["tool_call_id"] = strings.TrimSpace(message.ToolCallID)
	}
	if len(message.ToolCalls) > 0 {
		calls := make([]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   call.ID,
				"type": firstNonBlankString(call.Type, "function"),
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		raw["tool_calls"] = calls
	}
	if strings.TrimSpace(message.ReasoningContent) != "" {
		raw["reasoning_content"] = message.ReasoningContent
	}
	return raw
}

func (s *coderPlanningStream) planningPrompt() string {
	custom := strings.TrimSpace(s.settings.Plan.PrimaryPrompt())
	if s.engine != nil && strings.TrimSpace(s.engine.cfg.CoderPrompts.PlanningPrompt) != "" {
		custom = joinNonEmptyPrompts(custom, s.engine.cfg.CoderPrompts.PlanningPrompt)
	}
	executeToolDescriptions := s.buildExecuteToolDescriptions()
	hasExecuteToolDescriptionsPlaceholder := promptHasTemplateValue(custom, "execute_tool_descriptions")
	prompt := renderCoderPromptTemplate(custom, s.coderPromptTemplateValues(coderPromptTemplateData{
		AvailableTools:          s.planStageTools(),
		PlanStageTools:          s.planStageTools(),
		ExecuteStageTools:       s.executeStageTools(),
		ExecuteToolDescriptions: executeToolDescriptions,
	}))
	if executeToolDescriptions != "" && !hasExecuteToolDescriptionsPlaceholder {
		prompt += "\n\n" + executeToolDescriptions
	}
	return strings.TrimSpace(prompt)
}

func promptHasTemplateValue(prompt string, key string) bool {
	return strings.Contains(prompt, "{{"+key+"}}") || strings.Contains(prompt, "{{ "+key+" }}")
}

func (s *coderPlanningStream) coderPromptTemplateValues(data coderPromptTemplateData) map[string]string {
	return coderPromptTemplateValues(s.session, s.req, data)
}

func (s *coderPlanningStream) executionSystemPrompt(fallback string) string {
	return coderPlanningExecutionSystemPrompt(s.session, s.req, s.settings, s.planStageTools(), s.executeStageTools(), fallback)
}

func coderPlanningExecutionSystemPrompt(session QuerySession, req api.QueryRequest, settings PlanExecuteSettings, planTools []string, executeTools []string, fallback string) string {
	if planTools == nil {
		planTools = coderPlanningModePlanTools
	}
	if executeTools == nil {
		executeTools = coderPlanningExecuteTools(settings.Execute, session.ToolNames)
	}
	values := coderPromptTemplateValues(session, req, coderPromptTemplateData{
		AvailableTools:    executeTools,
		PlanStageTools:    planTools,
		ExecuteStageTools: executeTools,
	})
	stagePrompt := strings.TrimSpace(settings.Execute.PrimaryPrompt())
	if stagePrompt == "" {
		stagePrompt = fallback
	}
	stagePrompt = renderCoderPromptTemplate(stagePrompt, values)
	coderPrompt := renderCoderPromptTemplate(session.CoderSystemPrompt, values)
	return joinNonEmptyPrompts(coderPrompt, stagePrompt)
}

func joinNonEmptyPrompts(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n\n")
}

func planningStageHasAssistantText(messages []openAIMessage) bool {
	start := 0
	for index, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			start = index + 1
		}
	}
	for _, message := range messages[start:] {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		if openAIMessageContentHasText(message.Content) {
			return true
		}
	}
	return false
}

func openAIMessageContentHasText(content any) bool {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		for _, item := range value {
			part := AnyMapNode(item)
			if len(part) == 0 {
				if strings.TrimSpace(AnyStringNode(item)) != "" {
					return true
				}
				continue
			}
			if strings.TrimSpace(AnyStringNode(part["text"])) != "" {
				return true
			}
		}
	case []map[string]any:
		for _, part := range value {
			if strings.TrimSpace(AnyStringNode(part["text"])) != "" {
				return true
			}
		}
	}
	return false
}

func (s *coderPlanningStream) afterStageEOF() error {
	if !s.planDone {
		wasFeedback := s.currentPlanIsFeedback
		s.currentPlanIsFeedback = false
		s.planDone = true
		if s.execCtx == nil || s.execCtx.PlanningState == nil || strings.TrimSpace(s.execCtx.PlanningState.Markdown) == "" {
			if wasFeedback {
				s.summaryDone = true
				s.completed = true
				return nil
			}
			if planningStageHasAssistantText(s.executeMessages) {
				s.summaryDone = true
				s.completed = true
				return nil
			}
			s.pending = append(s.pending, DeltaError{
				Error: NewErrorPayload(
					"plan_not_created",
					"CODER planning mode ended without a Markdown plan",
					ErrorScopeRun,
					ErrorCategoryModel,
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
	planningID := ""
	planningFile := ""
	toolCallID := s.planConfirmationAwaitingID()
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningID = strings.TrimSpace(s.execCtx.PlanningState.PlanningID)
		planningFile = strings.TrimSpace(s.execCtx.PlanningState.PlanningFile)
		if id := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); id != "" {
			toolCallID = id
		}
	}
	return DeltaAwaitAsk{
		AwaitingID:   toolCallID,
		Mode:         "plan",
		Timeout:      0,
		RunID:        s.session.RunID,
		ViewportType: "builtin",
		ViewportKey:  "plan",
		Plan: map[string]any{
			"id":           "confirm",
			"planningId":   planningID,
			"planningFile": planningFile,
			"title":        "实施此计划？",
			"options": []any{
				map[string]any{"label": "是，实施此计划", "decision": "approve"},
				map[string]any{
					"label":    "否，请告知如何调整",
					"decision": "reject",
					"input": map[string]any{
						"type":        "text",
						"placeholder": "请告知如何调整",
						"required":    false,
					},
				},
			},
		},
	}
}

func (s *coderPlanningStream) awaitPlanConfirmation() error {
	s.confirmationPending = false
	s.confirmationDone = true
	awaitingID := s.planConfirmationAwaitingID()
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		if toolCallID := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); toolCallID != "" {
			awaitingID = toolCallID
		}
	}
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
			Answer:     AwaitingErrorAnswer("plan", "invalid_submit", err.Error()),
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
		SubmitID:   submitResult.Request.SubmitID,
		Params:     submitResult.Request.Params,
	})

	args := s.planConfirmationArgs()
	normalized, normalizeErr := normalizeHITLPlanSubmit(args, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     awaitingAnswerWithSubmitID(AwaitingErrorAnswer("plan", "invalid_submit", normalizeErr.Error()), submitResult.Request.SubmitID),
		})
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     awaitingAnswerWithSubmitID(normalized, submitResult.Request.SubmitID),
	})
	s.appendPlanConfirmationToolResult(normalized)

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
	switch confirmationDecision(normalized) {
	case "approve":
		return nil
	case "reject":
		s.preparePlanningFeedback(normalized)
		return nil
	default:
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
}

func (s *coderPlanningStream) appendPlanConfirmationToolResult(normalized map[string]any) {
	if s == nil || len(normalized) == 0 {
		return
	}
	toolID := s.planConfirmationAwaitingID()
	toolName := FinalizePlanningToolName
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		if value := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); value != "" {
			toolID = value
		}
		if value := strings.TrimSpace(s.execCtx.PlanningState.ToolName); value != "" {
			toolName = value
		}
	}
	content := MarshalJSON(normalized)
	result := ToolExecutionResult{
		Output:     content,
		Structured: CloneMap(normalized),
		ExitCode:   0,
	}
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   toolID,
		ToolName: toolName,
		Result:   result,
	})
	s.executeMessages = append(s.executeMessages, openAIMessage{
		Role:       "tool",
		ToolCallID: toolID,
		Name:       toolName,
		Content:    content,
	})
}

func (s *coderPlanningStream) planConfirmationArgs() map[string]any {
	ask := s.planConfirmationAsk()
	return map[string]any{
		"mode": ask.Mode,
		"plan": CloneMap(ask.Plan),
	}
}

func (s *coderPlanningStream) planConfirmationAwaitingID() string {
	if s != nil && s.execCtx != nil && s.execCtx.PlanningState != nil {
		if toolCallID := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); toolCallID != "" {
			return toolCallID
		}
	}
	return fmt.Sprintf("%s_coder_plan_confirm_%d", s.session.RunID, s.currentPlanningRevision())
}

func (s *coderPlanningStream) currentPlanningRevision() int {
	if s == nil || s.execCtx == nil || s.execCtx.PlanningRevision <= 0 {
		return 1
	}
	return s.execCtx.PlanningRevision
}

func (s *coderPlanningStream) planningFeedbackPrompt() string {
	reason := strings.TrimSpace(s.rejectedPlanReason)
	if reason == "" {
		reason = "(empty)"
	}
	return strings.TrimSpace(joinNonEmptyPrompts(
		s.planningPrompt(),
		`You are handling feedback on a CODER plan that the user rejected.

Rules:
1. Do not execute or mutate anything in this stage.
2. Use the rejected plan and feedback to decide what to do next.
3. If a revised plan should be proposed, call finalize_planning exactly once with a complete replacement Markdown plan for the next revision.
4. If the right outcome is to cancel or stop, do not call finalize_planning; reply with a concise cancellation or clarification note.
5. The backend will ask the user to confirm any revised plan before execution tools are available.`,
		"Original request:\n"+s.req.Message,
		"Rejected plan markdown:\n"+strings.TrimSpace(s.rejectedPlanMarkdown),
		"User decision: "+firstNonBlankString(s.rejectedPlanDecision, "reject"),
		"User feedback:\n"+reason,
	))
}

func confirmationDecision(normalized map[string]any) string {
	plan := AnyMapNode(normalized["plan"])
	return strings.ToLower(strings.TrimSpace(AnyStringNode(plan["decision"])))
}

func confirmationReason(normalized map[string]any) string {
	plan := AnyMapNode(normalized["plan"])
	return strings.TrimSpace(AnyStringNode(plan["reason"]))
}

func (s *coderPlanningStream) preparePlanningFeedback(normalized map[string]any) {
	markdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		markdown = s.execCtx.PlanningState.Markdown
	}
	s.rejectedPlanMarkdown = markdown
	s.rejectedPlanDecision = confirmationDecision(normalized)
	s.rejectedPlanReason = confirmationReason(normalized)
	if s.execCtx != nil {
		s.execCtx.PlanningState = nil
		s.execCtx.PlanningRevision = s.currentPlanningRevision() + 1
	}
	s.planDone = false
	s.confirmationDone = false
	s.nextPlanIsFeedback = true
	s.summaryBaseMessages = nil
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *coderPlanningStream) cancelUnstartedPlan(message string) {
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
	systemPrompt := s.executionSystemPrompt(defaultCoderExecuteSystemPrompt)
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
	systemPrompt := s.executionSystemPrompt(defaultCoderExecuteSystemPrompt)
	messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, openAIMessage{Role: "user", Content: taskPrompt})

	req := s.req
	req.Message = taskPrompt
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:   s.execCtx,
		Messages:  messages,
		ToolNames: s.executeStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:  s.settings.MaxWorkRoundsPerTask,
		Stage:     fmt.Sprintf("coder-execute-step-%d", s.taskIndex+1),
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
	planningMarkdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningMarkdown = s.execCtx.PlanningState.Markdown
	}
	summaryMessages := s.summaryMessages(planningMarkdown)

	stream, err := s.engine.newRunStreamWithOptions(s.ctx, s.req, s.sessionForStage(s.settings.Summary, nil), false, runStreamOptions{
		ExecCtx:                      s.execCtx,
		Messages:                     summaryMessages,
		ToolNames:                    nil,
		ModelKey:                     s.resolveStageModelKey(s.settings.Summary),
		MaxSteps:                     1,
		Stage:                        "coder-summary",
		PreserveProvidedSystemPrompt: true,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) summaryMessages(planningMarkdown string) []openAIMessage {
	base := append([]openAIMessage(nil), s.summaryBaseMessages...)
	if len(base) == 0 {
		base = append(base, openAIMessage{
			Role:    "system",
			Content: s.executionSystemPrompt(defaultCoderExecuteSystemPrompt),
		})
		base = append(base, s.executeMessages...)
	}
	return append(base, openAIMessage{
		Role:    "user",
		Content: s.renderSummaryUserPrompt(planningMarkdown),
	})
}

func (s *coderPlanningStream) renderSummaryUserPrompt(planningMarkdown string) string {
	template := defaultCoderSummaryUserPromptTemplate
	if s.engine != nil && strings.TrimSpace(s.engine.cfg.CoderPrompts.SummaryUserPromptTemplate) != "" {
		template = strings.TrimSpace(s.engine.cfg.CoderPrompts.SummaryUserPromptTemplate)
	}
	return strings.TrimSpace(renderTemplate(template, map[string]string{
		"original_request": s.req.Message,
		"confirmed_plan":   planningMarkdown,
	}))
}

func (s *coderPlanningStream) emitTaskTerminal(task *PlanTask, status string) {
	switch status {
	case "completed":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID})
	case "canceled":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID})
	case "failed":
		s.pending = append(s.pending, DeltaTaskLifecycle{
			Kind:   "error",
			TaskID: task.TaskID,
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
		Kind:   "error",
		TaskID: task.TaskID,
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
	return coderPlanningExecuteTools(s.settings.Execute, s.session.ToolNames)
}

func coderPlanningExecuteTools(stage StageSettings, toolNames []string) []string {
	tools := stageToolsOrDefault(stage, toolNames)
	return removeToolNames(tools, "plan_add_tasks", "plan_get_tasks", "plan_update_task", FinalizePlanningToolName, "ask_user_question")
}

func isPlanningOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "plan_add_tasks", "plan_get_tasks", "plan_update_task", FinalizePlanningToolName, "ask_user_question":
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
	if s.engine == nil || s.engine.tools == nil {
		return map[string]string{}
	}
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
