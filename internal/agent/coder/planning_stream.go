package coder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	hitlplan "agent-platform/internal/hitl/plan"
	"agent-platform/internal/i18n"
)

const DefaultExecuteSystemPrompt = `Execute the confirmed CODER plan for the user.`

const defaultCoderExecuteSystemPrompt = DefaultExecuteSystemPrompt

const defaultTaskExecutionPromptTemplate = `Task list:
{{task_list}}
Current task ID: {{task_id}}
Current task description: {{task_description}}
Execution rules:
1) Call at most one tool per round.
2) You may call any available tool as needed.
3) Before finishing this task, you MUST call plan_update_task to update its status.`

type coderPlanningStream struct {
	runtime Runtime
	ctx     context.Context
	req     api.QueryRequest
	session contracts.QuerySession
	execCtx *contracts.ExecutionContext

	settings contracts.PlanExecuteSettings
	pending  []contracts.AgentDelta
	current  contracts.AgentStream

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

	executeMessages []contracts.ModelMessage
}

func NewPlanningStream(runtime Runtime, ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	if runtime == nil {
		return nil, fmt.Errorf("coder planning runtime is nil")
	}
	runtimeSettings := runtime.Settings()
	settings := resolvePlanExecuteRuntimeSettings(session, runtimeSettings.DefaultPlanMaxSteps, runtimeSettings.DefaultPlanMaxWorkRoundsPerTask)
	execCtx := &contracts.ExecutionContext{
		Request:          req,
		Session:          session,
		RunControl:       contracts.RunControlFromContext(ctx),
		Budget:           contracts.NormalizeBudget(session.ResolvedBudget),
		StageSettings:    settings,
		RunLoopState:     contracts.RunLoopStateIdle,
		PlanningRevision: 1,
	}
	return &coderPlanningStream{
		runtime:  runtime,
		ctx:      ctx,
		req:      req,
		session:  session,
		execCtx:  execCtx,
		settings: settings,
		pending: []contracts.AgentDelta{
			contracts.DeltaStageMarker{Stage: "coder-plan"},
		},
	}, nil
}

func (s *coderPlanningStream) Next() (contracts.AgentDelta, error) {
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
			if messageStream, ok := s.current.(AccumulatedMessageStream); ok {
				accumulated := messageStream.AccumulatedMessages()
				if !s.planDone || !s.executionDone {
					if s.currentPlanIsFeedback {
						s.executeMessages = nonSystemMessages(accumulated)
					} else {
						s.executeMessages = append(s.executeMessages, nonSystemMessages(accumulated)...)
					}
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
		s.summaryDone = true
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
	stageSession.CurrentMessages = s.runtime.BuildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, stageSession, true, StageRunOptions{
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
	s.pending = append(s.pending, contracts.DeltaStageMarker{Stage: "coder-plan-feedback"})
	req := s.req
	req.Message = s.planningFeedbackPrompt()
	stageSession := s.sessionForStage(s.settings.Plan, s.planStageTools())
	stageSession.HistoryMessages = append(stageSession.HistoryMessages, rawMessagesFromModelMessages(s.executeMessages)...)
	stageSession.CurrentMessages = s.runtime.BuildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, stageSession, true, StageRunOptions{
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

func rawMessagesFromModelMessages(messages []contracts.ModelMessage) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		raw := rawMessageFromModelMessage(message)
		if len(raw) > 0 {
			out = append(out, raw)
		}
	}
	return out
}

func rawMessageFromModelMessage(message contracts.ModelMessage) map[string]any {
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
	if s.runtime != nil && strings.TrimSpace(s.runtime.Settings().PlanningPrompt) != "" {
		custom = joinNonEmptyPrompts(custom, s.runtime.Settings().PlanningPrompt)
	}
	executeToolDescriptions := s.buildExecuteToolDescriptions()
	hasExecuteToolDescriptionsPlaceholder := promptHasTemplateValue(custom, "execute_tool_descriptions")
	prompt := RenderPromptTemplate(custom, s.coderPromptTemplateValues(PromptTemplateData{
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

func (s *coderPlanningStream) coderPromptTemplateValues(data PromptTemplateData) map[string]string {
	return PromptTemplateValues(s.session, s.req, data)
}

func (s *coderPlanningStream) executionSystemPrompt(fallback string) string {
	return PlanningExecutionSystemPrompt(s.session, s.req, s.settings, s.planStageTools(), s.executeStageTools(), fallback)
}

func PlanningExecutionSystemPrompt(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings, planTools []string, executeTools []string, fallback string) string {
	if planTools == nil {
		planTools = PlanningModePlanTools()
	}
	if executeTools == nil {
		executeTools = PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
	}
	values := PromptTemplateValues(session, req, PromptTemplateData{
		AvailableTools:    executeTools,
		PlanStageTools:    planTools,
		ExecuteStageTools: executeTools,
	})
	stagePrompt := strings.TrimSpace(settings.Execute.PrimaryPrompt())
	if stagePrompt == "" {
		stagePrompt = fallback
	}
	stagePrompt = RenderPromptTemplate(stagePrompt, values)
	coderPrompt := RenderPromptTemplate(session.CoderSystemPrompt, values)
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

func planningStageHasAssistantText(messages []contracts.ModelMessage) bool {
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
		if modelMessageContentHasText(message.Content) {
			return true
		}
	}
	return false
}

func modelMessageContentHasText(content any) bool {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		for _, item := range value {
			part := contracts.AnyMapNode(item)
			if len(part) == 0 {
				if strings.TrimSpace(contracts.AnyStringNode(item)) != "" {
					return true
				}
				continue
			}
			if strings.TrimSpace(contracts.AnyStringNode(part["text"])) != "" {
				return true
			}
		}
	case []map[string]any:
		for _, part := range value {
			if strings.TrimSpace(contracts.AnyStringNode(part["text"])) != "" {
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
			s.pending = append(s.pending, contracts.DeltaError{
				Error: contracts.NewErrorPayload(
					"plan_not_created",
					"CODER planning mode ended without a Markdown plan",
					contracts.ErrorScopeRun,
					contracts.ErrorCategoryModel,
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
		s.summaryDone = true
		s.completed = true
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

func (s *coderPlanningStream) planConfirmationAsk() contracts.DeltaAwaitAsk {
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
	return contracts.DeltaAwaitAsk{
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
			"options": []any{
				map[string]any{"decision": "approve"},
				map[string]any{"decision": "reject"},
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
		return contracts.ErrRunControlUnavailable
	}
	defer s.execCtx.RunControl.ClearExpectedSubmit(awaitingID)

	s.execCtx.RunLoopState = contracts.RunLoopStateWaitingSubmit
	s.execCtx.RunControl.TransitionState(contracts.RunLoopStateWaitingSubmit)
	submitResult, err := s.execCtx.RunControl.AwaitSubmitWithTimeout(s.ctx, awaitingID, 0)
	if err != nil {
		if errors.Is(err, contracts.ErrRunInterrupted) {
			s.pending = append(s.pending, contracts.DeltaRunCancel{RunID: s.session.RunID})
			s.completed = true
			return nil
		}
		s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     contracts.AwaitingErrorAnswer("plan", "invalid_submit", err.Error()),
		})
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}

	s.execCtx.RunLoopState = contracts.RunLoopStateToolExecuting
	s.execCtx.RunControl.TransitionState(contracts.RunLoopStateToolExecuting)
	s.pending = append(s.pending, contracts.DeltaRequestSubmit{
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
		s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     awaitingAnswerWithSubmitID(contracts.AwaitingErrorAnswer("plan", "invalid_submit", normalizeErr.Error()), submitResult.Request.SubmitID),
		})
		s.cancelUnstartedPlan("已取消执行计划。")
		return nil
	}
	s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     awaitingAnswerWithSubmitID(normalized, submitResult.Request.SubmitID),
	})
	s.appendPlanConfirmationToolResult(normalized)

	if strings.EqualFold(contracts.AnyStringNode(normalized["status"]), "error") {
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
	toolName := contracts.FinalizePlanningToolName
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		if value := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); value != "" {
			toolID = value
		}
		if value := strings.TrimSpace(s.execCtx.PlanningState.ToolName); value != "" {
			toolName = value
		}
	}
	content := contracts.MarshalJSON(normalized)
	result := contracts.ToolExecutionResult{
		Output:     content,
		Structured: contracts.CloneMap(normalized),
		ExitCode:   0,
	}
	s.pending = append(s.pending, contracts.DeltaToolResult{
		ToolID:   toolID,
		ToolName: toolName,
		Result:   result,
	})
	s.executeMessages = append(s.executeMessages, contracts.ModelMessage{
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
		"plan": contracts.CloneMap(ask.Plan),
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
	plan := contracts.AnyMapNode(normalized["plan"])
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(plan["decision"])))
}

func confirmationReason(normalized map[string]any) string {
	plan := contracts.AnyMapNode(normalized["plan"])
	return strings.TrimSpace(contracts.AnyStringNode(plan["reason"]))
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
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func coderExecuteSyntheticQueryMessage(locale string) string {
	if i18n.ResolveLocale(i18n.DefaultLocale, locale) == i18n.LocaleZhCN {
		return "执行计划"
	}
	return "Execute plan"
}

func (s *coderPlanningStream) cancelUnstartedPlan(message string) {
	if strings.TrimSpace(message) != "" {
		s.pending = append(s.pending, contracts.DeltaContent{Text: message})
	}
	s.summaryDone = true
	s.completed = true
}

func (s *coderPlanningStream) startExecutionStage() error {
	planningMarkdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningMarkdown = s.execCtx.PlanningState.Markdown
	}
	executePrompt := "Execute the confirmed CODER plan.\n\nOriginal request:\n" + s.req.Message + "\n\nConfirmed plan:\n" + planningMarkdown
	executeProfiles := s.executeSystemInitProfiles()
	s.pending = append(s.pending,
		contracts.DeltaStageMarker{Stage: "coder-execute"},
		contracts.DeltaSyntheticQuery{
			ChatID:  s.session.ChatID,
			Role:    "user",
			Message: coderExecuteSyntheticQueryMessage(s.session.Locale),
			Stage:   "coder-execute",
			Source:  "coder-plan-approve",
			Messages: []map[string]any{{
				"role":    "user",
				"content": executePrompt,
			}},
			Systems: systemInitProfilePayloads(executeProfiles),
		},
	)
	messages := make([]contracts.ModelMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.executionSystemPrompt(defaultCoderExecuteSystemPrompt)
	messages = append(messages, contracts.ModelMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, contracts.ModelMessage{Role: "user", Content: executePrompt})

	req := s.req
	req.Message = executePrompt
	stageSession := s.sessionForStage(s.settings.Execute, s.executeStageTools())
	stageSession.SystemInitCache = mergeSystemInitProfileCache(stageSession.SystemInitCache, executeProfiles)
	stageSession.CurrentMessages = []map[string]any{{
		"role":    "user",
		"content": executePrompt,
	}}
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, stageSession, true, StageRunOptions{
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

func (s *coderPlanningStream) executeSystemInitProfiles() []contracts.SystemInitProfile {
	if s == nil || s.runtime == nil {
		return nil
	}
	session := s.session
	session.ResolvedStageSettings = s.settings
	return s.runtime.BuildExecuteSystemInitProfiles(session, s.req, s.settings)
}

func systemInitProfilePayloads(profiles []contracts.SystemInitProfile) []map[string]any {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, systemInitProfilePayload(profile))
	}
	return out
}

func systemInitProfilePayload(profile contracts.SystemInitProfile) map[string]any {
	return map[string]any{
		"cacheKey":       profile.CacheKey,
		"fingerprint":    profile.Fingerprint,
		"systemMessage":  cloneAnyMapViaJSON(profile.SystemMessage),
		"tools":          cloneAnySlice(profile.Tools),
		"model":          cloneAnyMapViaJSON(profile.Model),
		"toolChoice":     profile.ToolChoice,
		"requestOptions": cloneAnyMapViaJSON(profile.RequestOptions),
	}
}

func mergeSystemInitProfileCache(base map[string]contracts.SystemInitSnapshot, profiles []contracts.SystemInitProfile) map[string]contracts.SystemInitSnapshot {
	if len(profiles) == 0 {
		return base
	}
	out := make(map[string]contracts.SystemInitSnapshot, len(base)+len(profiles))
	for key, snapshot := range base {
		out[key] = snapshot
	}
	for _, profile := range profiles {
		if strings.TrimSpace(profile.CacheKey) == "" {
			continue
		}
		out[profile.CacheKey] = contracts.SystemInitSnapshot{
			Fingerprint:    profile.Fingerprint,
			SystemMessage:  cloneAnyMapViaJSON(profile.SystemMessage),
			Tools:          cloneAnySlice(profile.Tools),
			Model:          cloneAnyMapViaJSON(profile.Model),
			ToolChoice:     profile.ToolChoice,
			RequestOptions: cloneAnyMapViaJSON(profile.RequestOptions),
		}
	}
	return out
}

func (s *coderPlanningStream) advanceTaskExecution() error {
	task := &s.execCtx.PlanState.Tasks[s.taskIndex]
	if task.Status == "" || task.Status == "init" {
		task.Status = "in_progress"
	}
	s.execCtx.PlanState.ActiveTaskID = task.TaskID
	s.pending = append(s.pending,
		contracts.DeltaStageMarker{Stage: fmt.Sprintf("coder-execute-task-%d", s.taskIndex+1)},
		contracts.DeltaTaskLifecycle{
			Kind:        "start",
			TaskID:      task.TaskID,
			RunID:       s.session.RunID,
			TaskName:    task.TaskID,
			Description: task.Description,
		},
	)
	return s.startTaskStream(task)
}

func (s *coderPlanningStream) startTaskStream(task *contracts.PlanTask) error {
	beforeStatus := contracts.NormalizePlanTaskStatus(task.Status)
	taskPrompt := renderTemplate(defaultTaskTemplate(s.settings), map[string]string{
		"task_list":        formatTaskList(s.execCtx.PlanState.Tasks),
		"task_id":          task.TaskID,
		"task_description": task.Description,
	})
	messages := make([]contracts.ModelMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.executionSystemPrompt(defaultCoderExecuteSystemPrompt)
	messages = append(messages, contracts.ModelMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, contracts.ModelMessage{Role: "user", Content: taskPrompt})

	req := s.req
	req.Message = taskPrompt
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, StageRunOptions{
		ExecCtx:   s.execCtx,
		Messages:  messages,
		ToolNames: s.executeStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:  s.settings.MaxWorkRoundsPerTask,
		Stage:     fmt.Sprintf("coder-execute-step-%d", s.taskIndex+1),
		PostToolHook: func(toolName string, _ string) contracts.PostToolHookResult {
			if !isPlanTool(toolName) {
				return contracts.PostToolContinue
			}
			afterStatus := contracts.NormalizePlanTaskStatus(task.Status)
			if afterStatus != beforeStatus && isTerminalPlanStatus(afterStatus) {
				return contracts.PostToolStop
			}
			return contracts.PostToolContinue
		},
	})
	if err != nil {
		return err
	}
	s.current = stream
	s.taskLifecycle = true
	return nil
}

func (s *coderPlanningStream) emitTaskTerminal(task *contracts.PlanTask, status string) {
	switch status {
	case "completed":
		s.pending = append(s.pending, contracts.DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID})
	case "canceled":
		s.pending = append(s.pending, contracts.DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID})
	case "failed":
		s.pending = append(s.pending, contracts.DeltaTaskLifecycle{
			Kind:   "error",
			TaskID: task.TaskID,
			Error: contracts.NewErrorPayload("task_failed", "Task status updated to failed",
				contracts.ErrorScopeTask, contracts.ErrorCategorySystem, nil),
		})
	}
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *coderPlanningStream) emitTaskFailure(task *contracts.PlanTask, message string) {
	task.Status = "failed"
	s.pending = append(s.pending, contracts.DeltaPlanUpdate{
		PlanID: s.execCtx.PlanState.PlanID,
		ChatID: s.session.ChatID,
		Plan:   contracts.PlanTasksArray(s.execCtx.PlanState),
	})
	s.pending = append(s.pending, contracts.DeltaTaskLifecycle{
		Kind:   "error",
		TaskID: task.TaskID,
		Error: contracts.NewErrorPayload("task_execution_error", message,
			contracts.ErrorScopeTask, contracts.ErrorCategorySystem, map[string]any{"taskId": task.TaskID}),
	})
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *coderPlanningStream) planStageTools() []string {
	return PlanningModePlanTools()
}

func (s *coderPlanningStream) executeStageTools() []string {
	return PlanningExecuteToolsForStage(s.settings.Execute, s.session.ToolNames)
}

func PlanningExecuteToolsForStage(stage contracts.StageSettings, toolNames []string) []string {
	tools := stageToolsOrDefault(stage, toolNames)
	return PlanningExecuteTools(tools)
}

func isPlanningOnlyTool(name string) bool {
	return IsPlanningOnlyTool(name)
}

func (s *coderPlanningStream) planStagePostToolHook(toolName string, _ string) contracts.PostToolHookResult {
	if !isPlanningWriteTool(toolName) {
		return contracts.PostToolContinue
	}
	if s.execCtx != nil && s.execCtx.PlanningState != nil && strings.TrimSpace(s.execCtx.PlanningState.Markdown) != "" {
		return contracts.PostToolStop
	}
	return contracts.PostToolContinue
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
	if s.runtime == nil {
		return map[string]string{}
	}
	defs := s.runtime.ToolDefinitions()
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

func (s *coderPlanningStream) sessionForStage(stage contracts.StageSettings, toolNames []string) contracts.QuerySession {
	session := s.session
	if modelKey := s.resolveStageModelKey(stage); modelKey != "" {
		session.ModelKey = modelKey
	}
	if toolNames != nil {
		session.ToolNames = append([]string(nil), toolNames...)
	}
	return session
}

func (s *coderPlanningStream) resolveStageModelKey(stage contracts.StageSettings) string {
	if strings.TrimSpace(stage.ModelKey) != "" {
		return strings.TrimSpace(stage.ModelKey)
	}
	return s.session.ModelKey
}

func resolvePlanExecuteRuntimeSettings(session contracts.QuerySession, defaultMaxSteps int, defaultMaxWorkRoundsPerTask int) contracts.PlanExecuteSettings {
	settings := session.ResolvedStageSettings
	if settings.MaxSteps <= 0 || settings.MaxWorkRoundsPerTask <= 0 {
		settings = contracts.ResolvePlanExecuteSettings(session.StageSettings, defaultMaxSteps, defaultMaxWorkRoundsPerTask)
	}
	return settings
}

func isPlanTool(name string) bool {
	return contracts.IsPlanTaskToolName(name)
}

func isPlanningWriteTool(name string) bool {
	return contracts.IsFinalizePlanningToolName(name)
}

func isTerminalPlanStatus(status string) bool {
	switch status {
	case "completed", "canceled", "failed":
		return true
	default:
		return false
	}
}

func stageToolsOrDefault(stage contracts.StageSettings, fallback []string) []string {
	if len(stage.Tools) > 0 {
		return append([]string(nil), stage.Tools...)
	}
	return append([]string(nil), fallback...)
}

func nonSystemMessages(msgs []contracts.ModelMessage) []contracts.ModelMessage {
	out := make([]contracts.ModelMessage, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.Role) != "system" {
			out = append(out, msg)
		}
	}
	return out
}

func defaultTaskTemplate(settings contracts.PlanExecuteSettings) string {
	if strings.TrimSpace(settings.TaskExecutionPrompt) != "" {
		return settings.TaskExecutionPrompt
	}
	return defaultTaskExecutionPromptTemplate
}

func formatTaskList(tasks []contracts.PlanTask) string {
	if len(tasks) == 0 {
		return "- (empty)"
	}
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", task.TaskID, task.Status, task.Description))
	}
	return strings.Join(lines, "\n")
}

func normalizeHITLPlanSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitlplan.Normalize(args, params)
}

func awaitingAnswerWithSubmitID(answer map[string]any, submitID string) map[string]any {
	out := contracts.CloneMap(answer)
	if strings.TrimSpace(submitID) != "" {
		out["submitId"] = strings.TrimSpace(submitID)
	}
	return out
}

func awaitingContextFromDeltaAsk(awaitAsk contracts.DeltaAwaitAsk) contracts.AwaitingSubmitContext {
	return contracts.AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Plan),
		Timeout:    awaitAsk.Timeout,
	}
}

func awaitItemCount(mode string, questions []any, approvals []any, forms []any, plan map[string]any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form":
		return len(forms)
	case "plan":
		if len(plan) > 0 {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func cloneAnyMapViaJSON(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return contracts.CloneMap(values)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return contracts.CloneMap(values)
	}
	return out
}

func cloneAnySlice(value any) []any {
	items, _ := value.([]any)
	if len(items) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(items))
	for _, item := range items {
		if mapped := contracts.AnyMapNode(item); len(mapped) > 0 {
			cloned = append(cloned, cloneAnyMapViaJSON(mapped))
			continue
		}
		cloned = append(cloned, item)
	}
	return cloned
}
