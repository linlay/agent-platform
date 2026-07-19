package coder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"agent-platform/internal/hitl/planning"
	"agent-platform/internal/i18n"
)

const DefaultExecuteSystemPrompt = `Execute the confirmed CODER planning for the user.`

const defaultCoderExecuteSystemPrompt = DefaultExecuteSystemPrompt

type coderPlanningStream struct {
	runtime Runtime
	ctx     context.Context
	req     api.QueryRequest
	session contracts.QuerySession
	execCtx *contracts.ExecutionContext

	settings contracts.CoderPlanningSettings
	pending  []contracts.AgentDelta
	current  contracts.AgentStream

	planningDone              bool
	executionDone             bool
	confirmationPending       bool
	confirmationDone          bool
	summaryDone               bool
	completed                 bool
	closed                    bool
	nextPlanningIsFeedback    bool
	currentPlanningIsFeedback bool

	rejectedPlanningMarkdown string
	rejectedPlanningDecision string
	rejectedPlanningReason   string

	executeMessages []contracts.ModelMessage
}

func NewPlanningStream(runtime Runtime, ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	if runtime == nil {
		return nil, fmt.Errorf("coder planning runtime is nil")
	}
	runtimeSettings := runtime.Settings()
	settings := resolveCoderPlanningRuntimeSettings(session, runtimeSettings.DefaultPlanningMaxSteps)
	execCtx := &contracts.ExecutionContext{
		Request:               req,
		Session:               session,
		RunControl:            contracts.RunControlFromContext(ctx),
		Budget:                contracts.NormalizeBudget(session.ResolvedBudget),
		CoderPlanningSettings: settings,
		RunLoopState:          contracts.RunLoopStateIdle,
		PlanningRevision:      1,
		ToolExecutionPolicy:   session.ToolExecutionPolicy,
	}
	return &coderPlanningStream{
		runtime:  runtime,
		ctx:      ctx,
		req:      req,
		session:  session,
		execCtx:  execCtx,
		settings: settings,
		pending: []contracts.AgentDelta{
			contracts.DeltaStageMarker{Stage: PlanningStage},
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
			if err := s.awaitPlanningConfirmation(); err != nil {
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
				if !s.planningDone || !s.executionDone {
					if s.currentPlanningIsFeedback {
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
	if !s.planningDone {
		if s.nextPlanningIsFeedback {
			return s.startPlanningFeedbackStage()
		}
		return s.startPlanningStage()
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

func (s *coderPlanningStream) startPlanningStage() error {
	s.currentPlanningIsFeedback = false
	planningPrompt := s.planningPrompt()
	req := s.req
	req.Message = strings.TrimSpace(planningPrompt) + "\n\nUser request:\n" + s.req.Message
	stageSession := s.sessionForStage(s.settings.Planning, s.planningStageTools())
	stageSession.CurrentMessages = s.runtime.BuildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, stageSession, true, StageRunOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planningStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Planning),
		MaxSteps:     s.settings.MaxSteps,
		Stage:        PlanningStage,
		PostToolHook: s.planningStagePostToolHook,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *coderPlanningStream) startPlanningFeedbackStage() error {
	s.nextPlanningIsFeedback = false
	s.currentPlanningIsFeedback = true
	s.pending = append(s.pending, contracts.DeltaStageMarker{Stage: "coder-planning-feedback"})
	req := s.req
	req.Message = s.planningFeedbackPrompt()
	stageSession := s.sessionForStage(s.settings.Planning, s.planningStageTools())
	stageSession.HistoryMessages = append(stageSession.HistoryMessages, rawMessagesFromModelMessages(s.executeMessages)...)
	stageSession.CurrentMessages = s.runtime.BuildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.runtime.NewStageRunStream(s.ctx, req, stageSession, true, StageRunOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planningStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Planning),
		MaxSteps:     s.settings.MaxSteps,
		Stage:        "coder-planning-feedback",
		PostToolHook: s.planningStagePostToolHook,
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
	custom := strings.TrimSpace(s.settings.Planning.PrimaryPrompt())
	if s.runtime != nil && strings.TrimSpace(s.runtime.Settings().PlanningPrompt) != "" {
		custom = joinNonEmptyPrompts(custom, s.runtime.Settings().PlanningPrompt)
	}
	executeToolDescriptions := s.buildExecuteToolDescriptions()
	hasExecuteToolDescriptionsPlaceholder := promptHasTemplateValue(custom, "execute_tool_descriptions")
	prompt := RenderPromptTemplate(custom, s.coderPromptTemplateValues(PromptTemplateData{
		AvailableTools:          s.planningStageTools(),
		PlanningStageTools:      s.planningStageTools(),
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
	return PlanningExecutionSystemPrompt(s.session, s.req, s.settings, s.planningStageTools(), s.executeStageTools(), fallback)
}

func PlanningExecutionSystemPrompt(session contracts.QuerySession, req api.QueryRequest, settings contracts.CoderPlanningSettings, planningTools []string, executeTools []string, fallback string) string {
	if planningTools == nil {
		planningTools = PlanningModeTools()
	}
	if executeTools == nil {
		executeTools = PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
	}
	values := PromptTemplateValues(session, req, PromptTemplateData{
		AvailableTools:     executeTools,
		PlanningStageTools: planningTools,
		ExecuteStageTools:  executeTools,
	})
	stagePrompt := strings.TrimSpace(settings.Execute.PrimaryPrompt())
	if stagePrompt == "" {
		stagePrompt = fallback
	}
	stagePrompt = RenderPromptTemplate(stagePrompt, values)
	coderPrompt := RenderPromptTemplate(session.ModeSystemPrompt, values)
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
	if !s.planningDone {
		wasFeedback := s.currentPlanningIsFeedback
		s.currentPlanningIsFeedback = false
		s.planningDone = true
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
				Error: apperrors.Payload(
					apperrors.CodePlanningNotCreated,
					"CODER planning mode ended without a Markdown planning document",
				),
			})
			s.completed = true
			s.summaryDone = true
			return nil
		}
		s.emitPlanningConfirmationAsk()
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

func (s *coderPlanningStream) emitPlanningConfirmationAsk() {
	awaitAsk := s.planningConfirmationAsk()
	if s.execCtx != nil && s.execCtx.RunControl != nil {
		awaitingCtx := awaitingContextFromDeltaAsk(awaitAsk)
		awaitingCtx.NoTimeout = true
		s.execCtx.RunControl.ExpectSubmit(awaitingCtx)
	}
	s.pending = append(s.pending, awaitAsk)
	s.confirmationPending = true
}

func (s *coderPlanningStream) planningConfirmationAsk() contracts.DeltaAwaitAsk {
	planningID := ""
	planningFile := ""
	toolCallID := s.planningConfirmationAwaitingID()
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		planningID = strings.TrimSpace(s.execCtx.PlanningState.PlanningID)
		planningFile = strings.TrimSpace(s.execCtx.PlanningState.PlanningFile)
		if id := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); id != "" {
			toolCallID = id
		}
	}
	return contracts.DeltaAwaitAsk{
		AwaitingID:   toolCallID,
		Mode:         "planning",
		RunID:        s.session.RunID,
		ViewportType: "builtin",
		ViewportKey:  "planning",
		Planning: map[string]any{
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

func (s *coderPlanningStream) awaitPlanningConfirmation() error {
	s.confirmationPending = false
	s.confirmationDone = true
	awaitingID := s.planningConfirmationAwaitingID()
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
	waitStartedAt := time.Now()
	submitResult, err := s.execCtx.RunControl.AwaitSubmitIndefinitely(s.ctx, awaitingID)
	s.execCtx.BudgetPaused += time.Since(waitStartedAt)
	if err != nil {
		if errors.Is(err, contracts.ErrRunInterrupted) {
			s.pending = append(s.pending, contracts.DeltaRunCancel{RunID: s.session.RunID})
			s.completed = true
			return nil
		}
		s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     contracts.AwaitingErrorAnswer("planning", "invalid_submit", err.Error()),
		})
		s.cancelUnstartedPlanning("已取消执行 planning。")
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

	args := s.planningConfirmationArgs()
	normalized, normalizeErr := normalizePlanningConfirmationSubmit(args, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     awaitingAnswerWithSubmitID(contracts.AwaitingErrorAnswer("planning", "invalid_submit", normalizeErr.Error()), submitResult.Request.SubmitID),
		})
		s.cancelUnstartedPlanning("已取消执行 planning。")
		return nil
	}
	s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     awaitingAnswerWithSubmitID(normalized, submitResult.Request.SubmitID),
	})
	s.appendPlanningConfirmationToolResult(normalized)

	if strings.EqualFold(contracts.AnyStringNode(normalized["status"]), "error") {
		s.cancelUnstartedPlanning("已取消执行 planning。")
		return nil
	}
	switch confirmationDecision(normalized) {
	case "approve":
		if s.preparePlanningApproveContinuation(submitResult.Request, awaitingID, normalized) {
			return nil
		}
		return nil
	case "reject":
		s.preparePlanningFeedback(normalized)
		return nil
	default:
		s.cancelUnstartedPlanning("已取消执行 planning。")
		return nil
	}
}

func (s *coderPlanningStream) preparePlanningApproveContinuation(submitReq api.SubmitRequest, awaitingID string, normalized map[string]any) bool {
	continuationRunID := strings.TrimSpace(submitReq.ContinuationRunID)
	if continuationRunID == "" {
		return false
	}
	s.pending = append(s.pending, contracts.DeltaRunContinuation{
		SourceRunID:       s.session.RunID,
		RunID:             continuationRunID,
		ChatID:            s.session.ChatID,
		AgentKey:          s.session.AgentKey,
		TeamID:            s.session.TeamID,
		AwaitingID:        awaitingID,
		SubmitID:          submitReq.SubmitID,
		Locale:            s.session.Locale,
		Mode:              "planning",
		Params:            submitReq.Params,
		Answer:            contracts.CloneMap(normalized),
		ContinuationState: submitReq.ContinuationState,
	})
	s.executionDone = true
	s.summaryDone = true
	s.completed = true
	return true
}

func (s *coderPlanningStream) appendPlanningConfirmationToolResult(normalized map[string]any) {
	if s == nil || len(normalized) == 0 {
		return
	}
	toolID := s.planningConfirmationAwaitingID()
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

func (s *coderPlanningStream) planningConfirmationArgs() map[string]any {
	ask := s.planningConfirmationAsk()
	return map[string]any{
		"mode":     ask.Mode,
		"planning": contracts.CloneMap(ask.Planning),
	}
}

func (s *coderPlanningStream) planningConfirmationAwaitingID() string {
	if s != nil && s.execCtx != nil && s.execCtx.PlanningState != nil {
		if toolCallID := strings.TrimSpace(s.execCtx.PlanningState.ToolCallID); toolCallID != "" {
			return toolCallID
		}
	}
	return fmt.Sprintf("%s_coder_planning_confirm_%d", s.session.RunID, s.currentPlanningRevision())
}

func (s *coderPlanningStream) currentPlanningRevision() int {
	if s == nil || s.execCtx == nil || s.execCtx.PlanningRevision <= 0 {
		return 1
	}
	return s.execCtx.PlanningRevision
}

func (s *coderPlanningStream) planningFeedbackPrompt() string {
	reason := strings.TrimSpace(s.rejectedPlanningReason)
	if reason == "" {
		reason = "(empty)"
	}
	return strings.TrimSpace(joinNonEmptyPrompts(
		s.planningPrompt(),
		`You are handling feedback on a CODER planning proposal that the user rejected.

Rules:
1. Do not execute or mutate anything in this stage.
2. Use the rejected planning and feedback to decide what to do next.
3. If a revised planning proposal should be made, call finalize_planning exactly once with a complete replacement Markdown planning document for the next revision.
4. If the right outcome is to cancel or stop, do not call finalize_planning; reply with a concise cancellation or clarification note.
5. The backend will ask the user to confirm any revised planning before execution tools are available.`,
		"Original request:\n"+s.req.Message,
		"Rejected planning markdown:\n"+strings.TrimSpace(s.rejectedPlanningMarkdown),
		"User decision: "+firstNonBlankString(s.rejectedPlanningDecision, "reject"),
		"User feedback:\n"+reason,
	))
}

func confirmationDecision(normalized map[string]any) string {
	planning := contracts.AnyMapNode(normalized["planning"])
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(planning["decision"])))
}

func confirmationReason(normalized map[string]any) string {
	planning := contracts.AnyMapNode(normalized["planning"])
	return strings.TrimSpace(contracts.AnyStringNode(planning["reason"]))
}

func (s *coderPlanningStream) preparePlanningFeedback(normalized map[string]any) {
	markdown := ""
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		markdown = s.execCtx.PlanningState.Markdown
	}
	s.rejectedPlanningMarkdown = markdown
	s.rejectedPlanningDecision = confirmationDecision(normalized)
	s.rejectedPlanningReason = confirmationReason(normalized)
	if s.execCtx != nil {
		s.execCtx.PlanningState = nil
		s.execCtx.PlanningRevision = s.currentPlanningRevision() + 1
	}
	s.planningDone = false
	s.confirmationDone = false
	s.nextPlanningIsFeedback = true
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ExecuteSyntheticQueryMessage(locale string) string {
	if i18n.ResolveLocale(i18n.DefaultLocale, locale) == i18n.LocaleZhCN {
		return "执行计划"
	}
	return "Execute planning"
}

func (s *coderPlanningStream) cancelUnstartedPlanning(message string) {
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
	executePrompt := PlanningApproveExecutePrompt(s.req.Message, planningMarkdown)
	executeProfiles := s.executeSystemInitProfiles()
	stageSession := s.sessionForStage(s.settings.Execute, s.executeStageTools())
	stageSession.SystemInitCache = mergeSystemInitProfileCache(stageSession.SystemInitCache, executeProfiles)
	executeSystem := contracts.TakePendingSystemInitPayload(&stageSession, ExecuteCacheKey)
	s.pending = append(s.pending,
		contracts.DeltaStageMarker{Stage: "coder-execute"},
		contracts.DeltaSyntheticQuery{
			ChatID:  s.session.ChatID,
			Role:    "user",
			Message: ExecuteSyntheticQueryMessage(s.session.Locale),
			Messages: []map[string]any{{
				"role":    "user",
				"content": executePrompt,
			}},
			System: executeSystem,
		},
	)
	messages := make([]contracts.ModelMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.executionSystemPrompt(defaultCoderExecuteSystemPrompt)
	messages = append(messages, contracts.ModelMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, contracts.ModelMessage{Role: "user", Content: executePrompt})

	req := s.req
	req.Message = executePrompt
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
	session.ResolvedCoderPlanningSettings = s.settings
	return s.runtime.BuildExecuteSystemInitProfiles(session, s.req, s.settings)
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
			AgentKey:       profile.AgentKey,
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

func (s *coderPlanningStream) planningStageTools() []string {
	return PlanningModeTools()
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

func (s *coderPlanningStream) planningStagePostToolHook(toolName string, _ string) contracts.PostToolHookResult {
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
	return "Tools available only after the user confirms the planning:\n" + strings.Join(lines, "\n")
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

func resolveCoderPlanningRuntimeSettings(session contracts.QuerySession, defaultMaxSteps int) contracts.CoderPlanningSettings {
	settings := session.ResolvedCoderPlanningSettings
	if settings.MaxSteps <= 0 {
		settings = contracts.ResolveCoderPlanningSettings(session.StageSettings, defaultMaxSteps)
	}
	return settings
}

func isPlanningWriteTool(name string) bool {
	return contracts.IsFinalizePlanningToolName(name)
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

func normalizePlanningConfirmationSubmit(args map[string]any, params any) (map[string]any, error) {
	return planning.NormalizeConfirmation(args, params)
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
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Planning),
		Questions:  append([]any(nil), awaitAsk.Questions...),
		Timeout:    awaitAsk.Timeout,
	}
}

func awaitItemCount(mode string, questions []any, approvals []any, forms []any, planning map[string]any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form":
		return len(forms)
	case "planning":
		if len(planning) > 0 {
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
