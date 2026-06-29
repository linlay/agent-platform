package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
)

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	admission, err := s.prepareQueryAdmission(r, true)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	prepared, err := s.completeQueryPreparation(r.Context(), admission, nil)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if isProxyRoutedAgent(prepared.agentDef) {
		if isNonStreamingQuery(prepared.req) && !isSyncQueryContext(r.Context()) {
			s.handleProxyQueryNonStream(w, r, prepared)
			return
		}
		if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "ws" {
			s.handleProxyWebSocketQuery(w, r, prepared)
			return
		}
		s.handleProxyQuery(w, r, prepared)
		return
	}
	if isSyncQueryContext(r.Context()) {
		s.handleQuerySync(w, r.Context(), prepared)
		return
	}
	if isNonStreamingQuery(prepared.req) {
		s.handleQueryNonStream(w, r.Context(), prepared)
		return
	}
	s.handleQueryAsync(w, r, prepared)
}

func isNonStreamingQuery(req api.QueryRequest) bool {
	return req.Stream != nil && !*req.Stream
}

func (s *Server) handleQueryAsync(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	locale := requestLocale(r, i18n.DefaultLocale)
	registered, statusErr := s.registerQueryRun(r.Context(), prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		writeStatusError(w, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	principal := PrincipalFromContext(r.Context())
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})

	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         stream.DefaultRenderConfig(),
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonStreamWriterFailed, err.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, err := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if err != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, err.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)

	StartRunExecutor(RunExecutorParams{
		RunCtx:             runCtx,
		Request:            prepared.req,
		Session:            prepared.session,
		Summary:            prepared.summary,
		Agent:              s.deps.Agent,
		Registry:           s.deps.Registry,
		Assembler:          assembler,
		Mapper:             mapper,
		Billing:            s.deps.Config.Billing,
		StepWriter:         stepWriter,
		EventBus:           eventBus,
		Chats:              s.deps.Chats,
		Models:             s.deps.Models,
		RunControl:         control,
		ResourceBaseURL:    prepared.resourceBaseURL,
		ResourceTickets:    s.ticketService,
		BuildQuerySession:  s.BuildQuerySession,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
		OnComplete: func(runID string) {
			releaseQuery(prepared.release)
			s.deps.Runs.Finish(runID)
			s.broadcast("run.finished", map[string]any{
				"runId":  runID,
				"chatId": prepared.req.ChatID,
			})
		},
	})

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", localizeStreamEventData(locale, event)); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleQuerySync(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	locale := i18n.ResolveLocale(i18n.DefaultLocale, responseLocale(w))
	registered, statusErr := s.registerQueryRun(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		writeStatusError(w, statusErr)
		return
	}
	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         stream.DefaultRenderConfig(),
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		releaseQuery(prepared.release)
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	if _, err := s.runQuerySync(ctx, prepared, registered, func(data stream.EventData) error {
		return sseWriter.WriteJSON("message", localizeStreamEventData(locale, data))
	}, nil); err == nil {
		_ = sseWriter.WriteDone()
	}
}

func (s *Server) handleQueryNonStream(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	registered, statusErr := s.registerQueryRun(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		writeStatusError(w, statusErr)
		return
	}
	collector := newQueryEventCollector(prepared.req.IncludeFullText)
	result, err := s.runQuerySync(ctx, prepared, registered, nil, collector.Consume)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	result = mergeObservedQueryRunResult(result, collector.Result())
	if prepared.req.IncludeFullText {
		result.FullText = collector.FullText(result.AssistantText)
	}
	if queryRunFailed(result) {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, queryRunErrorMessage(result), queryRunErrorPayload(result)))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(queryResponseFromResult(prepared.req, result)))
}

type queryRunResult struct {
	AssistantText string
	FinishReason  string
	Usage         chat.UsageData
	FullText      string
	ErrorMessage  string
	ErrorPayload  map[string]any
}

type queryEventCollector struct {
	assistantText strings.Builder
	finishReason  string
	usage         chat.UsageData
	fullText      *queryFullTextBuilder
	errorMessage  string
	errorPayload  map[string]any
}

func newQueryEventCollector(includeFullText bool) *queryEventCollector {
	c := &queryEventCollector{}
	if includeFullText {
		c.fullText = newQueryFullTextBuilder()
	}
	return c
}

func (c *queryEventCollector) Consume(event stream.EventData) {
	if c == nil {
		return
	}
	if c.fullText != nil {
		c.fullText.Consume(event)
	}
	switch event.Type {
	case "content.delta":
		if delta := event.String("delta"); delta != "" {
			c.assistantText.WriteString(delta)
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			c.assistantText.Reset()
			c.assistantText.WriteString(text)
		}
	case "content.end":
		if text := event.String("text"); text != "" && c.assistantText.Len() == 0 {
			c.assistantText.WriteString(text)
		}
	case "usage.snapshot":
		c.consumeUsage(event)
	case "run.complete":
		c.finishReason = "complete"
		c.consumeUsage(event)
	case "run.cancel":
		c.finishReason = "cancel"
		c.consumeUsage(event)
	case "run.error":
		c.finishReason = "error"
		if message := queryEventErrorMessage(event); message != "" {
			c.errorMessage = message
		}
		if payload := queryEventErrorPayload(event); len(payload) > 0 {
			c.errorPayload = payload
		}
		c.consumeUsage(event)
	}
}

func (c *queryEventCollector) Result() queryRunResult {
	if c == nil {
		return queryRunResult{FinishReason: "complete"}
	}
	finishReason := strings.TrimSpace(c.finishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	return queryRunResult{
		AssistantText: c.assistantText.String(),
		FinishReason:  finishReason,
		Usage:         c.usage,
		FullText:      c.FullText(c.assistantText.String()),
		ErrorMessage:  c.errorMessage,
		ErrorPayload:  cloneQueryErrorPayload(c.errorPayload),
	}
}

func (c *queryEventCollector) FullText(content string) string {
	if c == nil || c.fullText == nil {
		return ""
	}
	return c.fullText.Text(content)
}

type queryFullTextBuilder struct {
	parts             []string
	reasoningBuffers  map[string]*strings.Builder
	reasoningRecorded map[string]bool
	toolArgsBuffers   map[string]*strings.Builder
	toolNames         map[string]string
	toolRecorded      map[string]bool
}

func newQueryFullTextBuilder() *queryFullTextBuilder {
	return &queryFullTextBuilder{
		reasoningBuffers:  map[string]*strings.Builder{},
		reasoningRecorded: map[string]bool{},
		toolArgsBuffers:   map[string]*strings.Builder{},
		toolNames:         map[string]string{},
		toolRecorded:      map[string]bool{},
	}
}

func (b *queryFullTextBuilder) Consume(event stream.EventData) {
	if b == nil {
		return
	}
	switch event.Type {
	case "reasoning.delta":
		id := firstNonBlankString(event.String("reasoningId"), "reasoning")
		b.reasoningBuffer(id).WriteString(event.String("delta"))
	case "reasoning.end", "reasoning.snapshot":
		id := firstNonBlankString(event.String("reasoningId"), "reasoning")
		text := strings.TrimSpace(event.String("text"))
		if text == "" {
			text = strings.TrimSpace(b.reasoningBuffer(id).String())
		}
		b.appendOnce(b.reasoningRecorded, id, "Reasoning", text)
	case "tool.start":
		id := firstNonBlankString(event.String("toolId"), "tool")
		b.toolNames[id] = firstNonBlankString(event.String("toolName"), event.String("toolLabel"), id)
	case "tool.args":
		id := firstNonBlankString(event.String("toolId"), "tool")
		b.toolArgsBuffer(id).WriteString(event.String("delta"))
	case "tool.snapshot":
		id := firstNonBlankString(event.String("toolId"), "tool")
		name := firstNonBlankString(event.String("toolName"), b.toolNames[id], id)
		args := strings.TrimSpace(event.String("arguments"))
		if args == "" {
			args = strings.TrimSpace(b.toolArgsBuffer(id).String())
		}
		b.appendOnce(b.toolRecorded, id, "Tool: "+name, formatFullTextValue(args))
	case "tool.result":
		name := firstNonBlankString(event.String("toolName"), event.String("toolId"), "tool")
		b.appendPart("Tool result: "+name, formatFullTextValue(event.Value("result")))
	case "action.snapshot":
		name := firstNonBlankString(event.String("actionName"), event.String("actionId"), "action")
		b.appendPart("Action: "+name, formatFullTextValue(event.Value("arguments")))
	case "action.result":
		name := firstNonBlankString(event.String("actionId"), "action")
		b.appendPart("Action result: "+name, formatFullTextValue(event.Value("result")))
	case "planning.snapshot":
		b.appendPart("Plan", formatFullTextValue(event.Value("text")))
	case "planning.start":
		b.appendLine("Planning started")
	case "planning.end":
		b.appendLine("Planning finished")
	case "task.start":
		name := firstNonBlankString(event.String("taskName"), event.String("taskId"), "task")
		detail := strings.TrimSpace(event.String("description"))
		if detail != "" {
			name += ": " + detail
		}
		b.appendLine("Task started: " + name)
	case "task.complete":
		name := firstNonBlankString(event.String("taskName"), event.String("taskId"), "task")
		b.appendLine("Task completed: " + name)
	case "run.error":
		b.appendPart("Run error", formatFullTextValue(event.Value("error")))
	case "run.cancel":
		b.appendLine("Run canceled")
	}
}

func (b *queryFullTextBuilder) Text(content string) string {
	if b == nil {
		return strings.TrimSpace(content)
	}
	parts := append([]string(nil), b.parts...)
	if answer := strings.TrimSpace(content); answer != "" {
		parts = append(parts, "Answer\n"+answer)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func (b *queryFullTextBuilder) reasoningBuffer(id string) *strings.Builder {
	if existing := b.reasoningBuffers[id]; existing != nil {
		return existing
	}
	next := &strings.Builder{}
	b.reasoningBuffers[id] = next
	return next
}

func (b *queryFullTextBuilder) toolArgsBuffer(id string) *strings.Builder {
	if existing := b.toolArgsBuffers[id]; existing != nil {
		return existing
	}
	next := &strings.Builder{}
	b.toolArgsBuffers[id] = next
	return next
}

func (b *queryFullTextBuilder) appendOnce(seen map[string]bool, key string, title string, body string) {
	if seen[key] {
		return
	}
	seen[key] = true
	b.appendPart(title, body)
}

func (b *queryFullTextBuilder) appendPart(title string, body string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return
	}
	if body == "" {
		b.appendLine(title)
		return
	}
	if title == "" {
		b.appendLine(body)
		return
	}
	b.appendLine(title + "\n" + body)
}

func (b *queryFullTextBuilder) appendLine(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.parts = append(b.parts, text)
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatFullTextValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		data, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(typed))
		}
		return strings.TrimSpace(string(data))
	}
}

func (c *queryEventCollector) consumeUsage(event stream.EventData) {
	if c == nil || event.Payload == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		mergeUsageMapIntoRunData(&c.usage, run)
		return
	}
	mergeUsageMapIntoRunData(&c.usage, usage)
}

func queryResponseFromResult(req api.QueryRequest, result queryRunResult) api.QueryResponse {
	resp := api.QueryResponse{
		Content: result.AssistantText,
	}
	if req.IncludeFullText {
		fullText := strings.TrimSpace(result.FullText)
		if fullText == "" {
			fullText = strings.TrimSpace(result.AssistantText)
		}
		resp.FullText = &fullText
	}
	if req.IncludeUsage {
		resp.Usage = mapUsageDataPtr(&result.Usage)
	}
	return resp
}

func mergeObservedQueryRunResult(result queryRunResult, observed queryRunResult) queryRunResult {
	if strings.TrimSpace(result.AssistantText) == "" && strings.TrimSpace(observed.AssistantText) != "" {
		result.AssistantText = observed.AssistantText
	}
	if strings.EqualFold(strings.TrimSpace(observed.FinishReason), "error") || strings.TrimSpace(result.FinishReason) == "" {
		result.FinishReason = observed.FinishReason
	}
	if strings.TrimSpace(result.ErrorMessage) == "" {
		result.ErrorMessage = observed.ErrorMessage
	}
	if len(result.ErrorPayload) == 0 {
		result.ErrorPayload = cloneQueryErrorPayload(observed.ErrorPayload)
	}
	if result.Usage.TotalTokens == 0 && observed.Usage.TotalTokens > 0 {
		result.Usage = observed.Usage
	}
	return result
}

func queryRunFailed(result queryRunResult) bool {
	return strings.EqualFold(strings.TrimSpace(result.FinishReason), "error")
}

func queryRunErrorMessage(result queryRunResult) string {
	if message := strings.TrimSpace(result.ErrorMessage); message != "" {
		return message
	}
	return "query run failed"
}

func queryRunErrorPayload(result queryRunResult) map[string]any {
	if len(result.ErrorPayload) > 0 {
		return cloneQueryErrorPayload(result.ErrorPayload)
	}
	return apperrors.Payload(apperrors.CodeStreamFailed, queryRunErrorMessage(result), apperrors.WithScope(apperrors.ScopeRun))
}

func queryEventErrorPayload(event stream.EventData) map[string]any {
	payload, _ := event.Value("error").(map[string]any)
	return cloneQueryErrorPayload(payload)
}

func queryEventErrorMessage(event stream.EventData) string {
	if message := strings.TrimSpace(event.String("message")); message != "" {
		return message
	}
	if message := strings.TrimSpace(event.String("error")); message != "" {
		return message
	}
	return queryErrorValueMessage(event.Value("error"))
}

func queryErrorValueMessage(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"message", "error", "code"} {
			if message, _ := typed[key].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
	}
	return strings.TrimSpace(formatFullTextValue(value))
}

func cloneQueryErrorPayload(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *Server) runQuerySync(_ context.Context, prepared preparedQuery, registered registeredQueryRun, emitVisible func(stream.EventData) error, observeEvent func(stream.EventData)) (queryRunResult, error) {
	defer releaseQuery(prepared.release)
	control := registered.Control
	if control == nil {
		return queryRunResult{}, fmt.Errorf("run control unavailable")
	}
	control.SetObserverCount(1)
	runCtx := registered.RunCtx
	if runCtx == nil {
		runCtx = contracts.WithRunControl(control.Context(), control)
	}
	defer control.SetObserverCount(0)
	if registered.Managed {
		defer s.deps.Runs.Finish(prepared.req.RunID)
	}

	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})
	defer s.broadcast("run.finished", map[string]any{
		"runId":  prepared.req.RunID,
		"chatId": prepared.req.ChatID,
	})

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	principal := &Principal{Subject: prepared.session.Subject}
	if strings.TrimSpace(principal.Subject) == "" {
		principal = nil
	}

	var (
		assistantText strings.Builder
		chatUsage     chat.UsageData
		runUsage      chat.UsageData
	)
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	processor := &runEventProcessor{
		assistantText: &assistantText,
		stepWriter:    chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode),
		billing:       s.deps.Config.Billing,
		models:        s.deps.Models,
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}
	processor.stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	processor.stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	runCtx = chat.WithApprovalSummarySink(runCtx, processor.stepWriter.RecordApproval)
	writeEvent := func(event stream.StreamEvent) error {
		data, visible := processor.Consume(event)
		if observeEvent != nil {
			observeEvent(data)
		}
		if !visible {
			return nil
		}
		if emitVisible == nil {
			return nil
		}
		return emitVisible(clientVisibleEventData(data))
	}

	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return queryRunResult{}, err
		}
	}

	agentStream, err := s.deps.Agent.Stream(runCtx, prepared.req, prepared.session)
	if err != nil {
		control.TransitionState(contracts.RunLoopStateFailed)
		for _, event := range assembler.Fail(err) {
			if writeErr := writeEvent(event); writeErr != nil {
				return queryRunResult{}, writeErr
			}
		}
		persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "error", false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		return queryRunResult{
			AssistantText: assistantText.String(),
			FinishReason:  "error",
			Usage:         runUsage,
			ErrorMessage:  err.Error(),
			ErrorPayload:  apperrors.FromError(err, apperrors.CodeStreamFailed, apperrors.WithScope(apperrors.ScopeRun)),
		}, nil
	}
	defer agentStream.Close()

	streamFailed := false
	streamInterrupted := false
	var streamErr error
	for {
		delta, nextErr := agentStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			streamInterrupted = true
			break
		}
		if nextErr != nil {
			streamFailed = true
			streamErr = nextErr
			control.TransitionState(contracts.RunLoopStateFailed)
			for _, event := range assembler.Fail(nextErr) {
				if writeErr := writeEvent(event); writeErr != nil {
					return queryRunResult{}, writeErr
				}
			}
			break
		}
		inputs := mapper.Map(delta)
		for _, input := range inputs {
			for _, event := range assembler.Consume(input) {
				if err := writeEvent(event); err != nil {
					return queryRunResult{}, err
				}
			}
		}
	}

	processor.stepWriter.Flush()
	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		errorMessage := ""
		if streamErr != nil {
			errorMessage = streamErr.Error()
		}
		persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, finishReason, false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		return queryRunResult{
			AssistantText: assistantText.String(),
			FinishReason:  finishReason,
			Usage:         runUsage,
			ErrorMessage:  errorMessage,
			ErrorPayload:  apperrors.FromError(streamErr, apperrors.CodeStreamFailed, apperrors.WithScope(apperrors.ScopeRun)),
		}, nil
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return queryRunResult{}, err
		}
	}
	persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "complete", true)
	if persisted {
		syncBroadcastChatUpdated(s.deps.Notifications, completion)
	}
	return queryRunResult{AssistantText: assistantText.String(), FinishReason: "complete", Usage: runUsage}, nil
}

// syncRunExecutorParams 构造 handleQuerySync 三次持久化完成态调用所需参数
// 调用共用的 RunExecutorParams，避免重复拼装三份 callback。
func syncRunExecutorParams(s *Server, prepared preparedQuery, control *contracts.RunControl, principal *Principal) RunExecutorParams {
	return RunExecutorParams{
		Request:            prepared.req,
		Session:            prepared.session,
		Chats:              s.deps.Chats,
		RunControl:         control,
		ResourceBaseURL:    prepared.resourceBaseURL,
		ResourceTickets:    s.ticketService,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
	}
}

// syncBroadcastChatUpdated 复刻 run_executor.broadcastRunCompletion 的 chat.updated
// 广播语义。async 路径在 StartRunExecutor 内部走那条；sync 路径没经过 StartRunExecutor，
// 这里手动补上，让 automation 触发的 run 也能通知 hub（进而透传到 gateway / webclient）。
func syncBroadcastChatUpdated(notifications contracts.NotificationSink, completion chat.RunCompletion) {
	if notifications == nil {
		return
	}
	notifications.Broadcast("chat.updated", map[string]any{
		"chatId":         completion.ChatID,
		"lastRunId":      completion.RunID,
		"lastRunContent": completion.AssistantText,
		"updatedAt":      completion.UpdatedAtMillis,
	})
}
