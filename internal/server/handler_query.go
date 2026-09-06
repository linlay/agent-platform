package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	req, err := decodeQueryRequest(r)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	if !isSyncQueryContext(r.Context()) && !isNonStreamingQuery(req) {
		s.handleRuntimeQueryAsync(w, r, req)
		return
	}
	admission, err := s.prepareQueryAdmissionRequest(
		r.Context(), req, true, requestLocale(r, i18n.DefaultLocale), requestBaseURL(r),
	)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	prepared, err := s.completeQueryPreparation(r.Context(), admission, nil)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	prepared.session.WebClientTarget = webClientTargetFromHTTPRequest(r)
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
	s.handlePreparedLocalQuery(w, r, prepared)
}

func writeQueryStartError(w http.ResponseWriter, err error) {
	if isTimeContractViolation(err) {
		writeTimeContractViolation(w, err)
		return
	}
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		writeStatusError(w, statusErr)
		return
	}
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		status := apperrorsStatus(appErr, http.StatusInternalServerError)
		writeJSON(w, status, api.Failure(status, err.Error()))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) handleRuntimeQueryAsync(w http.ResponseWriter, r *http.Request, req api.QueryRequest) {
	command := queryCommandFromAPI(req)
	command.Locale = requestLocale(r, i18n.DefaultLocale)
	command.ResourceBaseURL = requestBaseURL(r)
	command.ChatSource = chatSourceFromContext(r.Context())
	command.ClientTarget = runtimeClientTarget(webClientTargetFromHTTPRequest(r))
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		command.Caller.Subject = strings.TrimSpace(principal.Subject)
	}
	s.writeRuntimeQueryStream(w, r.Context(), command)
}

func (s *Server) writeRuntimeQueryStream(w http.ResponseWriter, ctx context.Context, command runtimetypes.QueryCommand) {
	handle, err := s.deps.Runtime.StartQuery(ctx, command)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	sseWriter, err := newSSEWriter(w, sseWriterOptions{
		SSE: s.deps.Config.SSE, Render: stream.DefaultRenderConfig(),
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		_, _ = s.deps.Runtime.Interrupt(ctx, runtimeSetupInterrupt(handle, contracts.InterruptReasonStreamWriterFailed, err.Error()))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()
	subscription, err := s.deps.Runtime.AttachRun(ctx, runtimetypes.RunRef{
		RunID: handle.RunID, ChatID: handle.ChatID, AgentKey: handle.AgentKey, TeamID: handle.TeamID,
	}, 0)
	if err != nil {
		_, _ = s.deps.Runtime.Interrupt(ctx, runtimeSetupInterrupt(handle, contracts.InterruptReasonObserverAttachFailed, err.Error()))
		_ = sseWriter.WriteDone()
		return
	}
	defer subscription.Close()
	lastSeq := int64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-subscription.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", localizeStreamEventData(command.Locale, event)); err != nil {
				if isTimeContractViolation(err) {
					seq := event.Seq
					if seq <= lastSeq {
						seq = lastSeq + 1
					}
					local := localTimeContractRunErrorEvent(seq, handle.RunID, handle.ChatID, err)
					_ = sseWriter.WriteJSON("message", localizeStreamEventData(command.Locale, local))
					_ = sseWriter.WriteDone()
					_, _ = s.deps.Runtime.Interrupt(ctx, runtimeSetupInterrupt(handle, contracts.InterruptReasonRunInterrupted, timeContractViolationMessage))
				}
				return
			}
			lastSeq = event.Seq
		}
	}
}

func runtimeSetupInterrupt(handle runtimetypes.RunHandle, reason, detail string) runtimetypes.InterruptCommand {
	return runtimetypes.InterruptCommand{
		RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: handle.ChatID, AgentKey: handle.AgentKey, TeamID: handle.TeamID, Caller: runtimetypes.Caller{Scope: "server"}},
		Source: contracts.InterruptSourceServerSetup, Reason: reason, Detail: detail,
	}
}

func (s *Server) handlePreparedLocalQuery(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
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
	principal := PrincipalFromContext(r.Context())
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	sseWriter, err := newSSEWriter(w, sseWriterOptions{
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

	s.startPreparedLocalRun(prepared, registered, eventBus, principal)

	lastSeq := int64(0)
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
				if isTimeContractViolation(err) {
					seq := event.Seq
					if seq <= lastSeq {
						seq = lastSeq + 1
					}
					local := localTimeContractRunErrorEvent(seq, prepared.req.RunID, prepared.req.ChatID, err)
					_ = sseWriter.WriteJSON("message", localizeStreamEventData(locale, local))
					_ = sseWriter.WriteDone()
					// This is a defensive final boundary (the executor validates
					// before publishing). If another producer injected a bad event
					// directly into the bus, still cancel it rather than letting the
					// upstream run continue after the client has been terminated.
					if registered.Control != nil {
						registered.Control.Interrupt(contracts.InterruptInfo{
							Source: contracts.InterruptSourceHTTPAPI,
							Reason: contracts.InterruptReasonRunInterrupted,
							Detail: timeContractViolationMessage,
						})
						registered.Control.TransitionState(contracts.RunLoopStateFailed)
					}
				}
				return
			}
			lastSeq = event.Seq
		}
	}
}

func (s *Server) startPreparedLocalRun(prepared preparedQuery, registered registeredQueryRun, eventBus *stream.RunEventBus, principal *Principal) {
	StartRunExecutor(s.localRunExecutorParams(prepared, registered, eventBus, principal))
}

func (s *Server) localRunExecutorParams(
	prepared preparedQuery,
	registered registeredQueryRun,
	eventBus *stream.RunEventBus,
	principal *Principal,
) RunExecutorParams {
	execution := s.resolvedQueryExecution(prepared)
	if !execution.HiddenRun {
		s.broadcast("run.started", runStartedPushPayload(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey, registered.StartedAtMillis))
	}
	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(execution.StepLineStore, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.systemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	var onUnreadChanged func(chat.Summary)
	var onContinuation func(contracts.DeltaRunContinuation) (string, error)
	notifications := s.deps.Notifications
	if execution.HiddenRun {
		notifications = nil
	} else {
		onUnreadChanged = func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		}

		onContinuation = s.startRunContinuation
	}

	return RunExecutorParams{
		RunCtx:            registered.RunCtx,
		Request:           prepared.req,
		Session:           prepared.session,
		StartedAtMillis:   registered.StartedAtMillis,
		Summary:           prepared.summary,
		Agent:             s.deps.Agent,
		Registry:          s.deps.Registry,
		TeamSnapshot:      prepared.teamSnapshot,
		Assembler:         assembler,
		Mapper:            mapper,
		Billing:           s.deps.Config.Billing,
		StepWriter:        stepWriter,
		EventBus:          eventBus,
		Chats:             execution.CompletionStore,
		Models:            s.deps.Models,
		RunControl:        registered.Control,
		ResourceBaseURL:   prepared.resourceBaseURL,
		ResourceTickets:   s.ticketService,
		BuildQuerySession: s.BuildQuerySession,
		PrepareSystemInit: s.prepareSystemInitCache,
		Notifications:     notifications,
		OnUnreadChanged:   onUnreadChanged,
		OnContinuation:    onContinuation,
		OnComplete: func(completion chat.RunCompletion) {
			releaseQuery(prepared.release)
			s.finishRegisteredQueryRun(prepared, registered)
			if !execution.HiddenRun {
				s.broadcast("run.finished", runFinishedPushPayload(
					completion.RunID,
					prepared.req.ChatID,
					completion.FinishReason,
					completion.UpdatedAtMillis,
				))
			}
		},
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
	sseWriter, err := newSSEWriter(w, sseWriterOptions{
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

	lastSeq := int64(0)
	result, runErr := s.executePreparedLocalQuery(ctx, prepared, registered, func(data stream.EventData) error {
		if err := sseWriter.WriteJSON("message", localizeStreamEventData(locale, data)); err != nil {
			return err
		}
		lastSeq = data.Seq
		return nil
	}, nil)
	runErrorMessage := result.ErrorMessage
	if strings.TrimSpace(runErrorMessage) == "" && runErr != nil {
		runErrorMessage = runErr.Error()
	}
	notifyInternalQueryCompletion(ctx, result.Completion, runErrorMessage)
	if runErr == nil {
		_ = sseWriter.WriteDone()
		return
	}
	if isTimeContractViolation(runErr) {
		// Headers have already selected SSE, so this is the streaming equivalent
		// of HTTP 422: publish one platform-owned error event, then terminate.
		local := localTimeContractRunErrorEvent(lastSeq+1, prepared.req.RunID, prepared.req.ChatID, runErr)
		_ = sseWriter.WriteJSON("message", localizeStreamEventData(locale, local))
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
	var fullText *queryFullTextBuilder
	var observe func(stream.EventData)
	if prepared.req.IncludeFullText {
		fullText = newQueryFullTextBuilder()
		observe = fullText.Observe
	}
	result, err := s.executePreparedLocalQuery(ctx, prepared, registered, nil, observe)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if prepared.req.IncludeFullText {
		result.FullText = fullText.Text(result.AssistantText)
	}
	if queryRunFailed(result) {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, queryRunErrorMessage(result), queryRunErrorPayload(result)))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(queryResponsePayload(prepared, result)))
}

type queryRunResult struct {
	AssistantText string
	FinishReason  string
	Usage         chat.UsageData
	FullText      string
	ErrorMessage  string
	ErrorPayload  map[string]any
	Completion    *chat.RunCompletion
}

type queryEventCollector struct {
	assistantText  strings.Builder
	modelTurnText  strings.Builder
	modelTurnOpen  bool
	modelTurnDirty bool
	finishReason   string
	usage          chat.UsageData
	fullText       *queryFullTextBuilder
	errorMessage   string
	errorPayload   map[string]any
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
	if event.Type == "run.activity" && isDiscardIncompleteModelTurnRecovery(event.Value("recovery")) {
		c.modelTurnText.Reset()
		c.modelTurnOpen = true
		c.modelTurnDirty = false
		if c.fullText != nil {
			c.fullText.DiscardModelTurn(event.Value("recovery"))
		}
		return
	}
	if c.fullText != nil {
		c.fullText.Consume(event)
	}
	switch event.Type {
	case "llm.request":
		c.finishModelTurn()
		c.modelTurnOpen = true
		c.modelTurnDirty = false
	case "content.delta":
		if delta := event.String("delta"); delta != "" {
			c.modelTurnDirty = c.modelTurnOpen
			c.contentBuffer().WriteString(delta)
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			c.modelTurnDirty = c.modelTurnOpen
			buffer := c.contentBuffer()
			buffer.Reset()
			buffer.WriteString(text)
		}
	case "content.end":
		if text := event.String("text"); text != "" && c.contentBuffer().Len() == 0 {
			c.modelTurnDirty = c.modelTurnOpen
			c.contentBuffer().WriteString(text)
		}
	case "usage.snapshot":
		c.consumeUsage(event)
	case "run.complete":
		c.finishModelTurn()
		c.finishReason = "complete"
		c.consumeUsage(event)
	case "run.cancel":
		c.finishModelTurn()
		c.finishReason = "cancel"
		c.consumeUsage(event)
	case "run.error":
		c.finishModelTurn()
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

func (c *queryEventCollector) contentBuffer() *strings.Builder {
	if c.modelTurnOpen {
		return &c.modelTurnText
	}
	return &c.assistantText
}

func (c *queryEventCollector) finishModelTurn() {
	if !c.modelTurnOpen {
		return
	}
	if c.modelTurnDirty {
		c.assistantText.Reset()
		c.assistantText.WriteString(c.modelTurnText.String())
	}
	c.modelTurnText.Reset()
	c.modelTurnOpen = false
	c.modelTurnDirty = false
}

func (c *queryEventCollector) Result() queryRunResult {
	if c == nil {
		return queryRunResult{FinishReason: "complete"}
	}
	finishReason := strings.TrimSpace(c.finishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	assistantText := c.assistantText.String()
	if c.modelTurnOpen && c.modelTurnDirty {
		assistantText = c.modelTurnText.String()
	}
	return queryRunResult{
		AssistantText: assistantText,
		FinishReason:  finishReason,
		Usage:         c.usage,
		FullText:      c.FullText(assistantText),
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

// Observe retains internal recovery signals; public EventBus events alone
// cannot reconstruct discarded model attempts for a blocking fullText result.
func (b *queryFullTextBuilder) Observe(event stream.EventData) {
	if event.Type == "run.activity" && isDiscardIncompleteModelTurnRecovery(event.Value("recovery")) {
		b.DiscardModelTurn(event.Value("recovery"))
		return
	}
	b.Consume(event)
}

type queryFullTextBuilder struct {
	parts             []queryFullTextPart
	reasoningBuffers  map[string]*strings.Builder
	reasoningRecorded map[string]bool
	toolArgsBuffers   map[string]*strings.Builder
	toolNames         map[string]string
	toolRecorded      map[string]bool
}

type queryFullTextPart struct {
	kind string
	id   string
	text string
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
	if event.Type == "tool.result" && event.Value("internalOnly") == true {
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
		b.appendOnce(b.reasoningRecorded, "reasoning", id, "Reasoning", text)
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
		b.appendOnce(b.toolRecorded, "tool", id, "Tool: "+name, formatFullTextValue(args))
	case "tool.result":
		name := firstNonBlankString(event.String("toolName"), event.String("toolId"), "tool")
		b.appendPart("Tool result: "+name, formatFullTextValue(event.Value("result")))
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
	parts := make([]string, 0, len(b.parts)+1)
	for _, part := range b.parts {
		parts = append(parts, part.text)
	}
	if answer := strings.TrimSpace(content); answer != "" {
		parts = append(parts, "Answer\n"+answer)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func (b *queryFullTextBuilder) DiscardModelTurn(recovery any) {
	if b == nil {
		return
	}
	payload, _ := recovery.(map[string]any)
	reasoningIDs := stringSetFromAny(payload["reasoningIds"])
	toolIDs := stringSetFromAny(payload["toolIds"])
	for id := range reasoningIDs {
		delete(b.reasoningBuffers, id)
		delete(b.reasoningRecorded, id)
	}
	for id := range toolIDs {
		delete(b.toolArgsBuffers, id)
		delete(b.toolNames, id)
		delete(b.toolRecorded, id)
	}
	if len(reasoningIDs)+len(toolIDs) == 0 {
		return
	}
	filtered := b.parts[:0]
	for _, part := range b.parts {
		switch part.kind {
		case "reasoning":
			if reasoningIDs[part.id] {
				continue
			}
		case "tool":
			if toolIDs[part.id] {
				continue
			}
		}
		filtered = append(filtered, part)
	}
	b.parts = filtered
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

func (b *queryFullTextBuilder) appendOnce(seen map[string]bool, kind string, key string, title string, body string) {
	if seen[key] {
		return
	}
	seen[key] = true
	b.appendModelPart(kind, key, title, body)
}

func (b *queryFullTextBuilder) appendModelPart(kind string, id string, title string, body string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return
	}
	text := title
	if text == "" {
		text = body
	} else if body != "" {
		text += "\n" + body
	}
	b.parts = append(b.parts, queryFullTextPart{kind: kind, id: id, text: text})
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
	b.parts = append(b.parts, queryFullTextPart{text: text})
}

func isDiscardIncompleteModelTurnRecovery(value any) bool {
	recovery, _ := value.(map[string]any)
	return strings.TrimSpace(anyString(recovery["action"])) == "discard_incomplete_model_turn"
}

func stringSetFromAny(value any) map[string]bool {
	result := map[string]bool{}
	switch values := value.(type) {
	case []string:
		for _, item := range values {
			if item = strings.TrimSpace(item); item != "" {
				result[item] = true
			}
		}
	case []any:
		for _, raw := range values {
			if item := strings.TrimSpace(anyString(raw)); item != "" {
				result[item] = true
			}
		}
	}
	return result
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

func queryResponsePayload(prepared preparedQuery, result queryRunResult) any {
	queryResponse := queryResponseFromResult(prepared.req, result)
	execution := prepared.execution
	if execution == nil || strings.TrimSpace(execution.BTWID) == "" {
		return queryResponse
	}
	return api.BTWResponse{
		BTWID:        execution.BTWID,
		ParentChatID: execution.ParentChatID,
		RunID:        prepared.req.RunID,
		Content:      queryResponse.Content,
		FullText:     queryResponse.FullText,
		Usage:        queryResponse.Usage,
	}
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

// executePreparedLocalQuery waits on the same executor used by detached runs.
// A blocking caller remains an observer for its whole execution, preserving the
// existing run-control policy independently of HTTP request cancellation.
func (s *Server) executePreparedLocalQuery(ctx context.Context, prepared preparedQuery, registered registeredQueryRun, emitVisible func(stream.EventData) error, observeEvent func(stream.EventData)) (queryRunResult, error) {
	if registered.Control == nil {
		releaseQuery(prepared.release)
		s.finishRegisteredQueryRun(prepared, registered)
		return queryRunResult{}, fmt.Errorf("run control unavailable")
	}
	registered.Control.SetObserverCount(1)
	defer registered.Control.SetObserverCount(0)
	if registered.RunCtx == nil {
		registered.RunCtx = contracts.WithRunControl(registered.Control.Context(), registered.Control)
	}
	var eventBus *stream.RunEventBus
	if registered.Managed {
		eventBus, _ = s.deps.Runs.EventBus(prepared.req.RunID)
	}
	principal := PrincipalFromContext(ctx)
	if principal == nil && strings.TrimSpace(prepared.session.Subject) != "" {
		principal = &Principal{Subject: prepared.session.Subject}
	}
	params := s.localRunExecutorParams(prepared, registered, eventBus, principal)
	params.EmitVisible, params.ObserveEvent = emitVisible, observeEvent
	result := runExecutor(params)
	completion := result.Completion
	return queryRunResult{
		AssistantText: completion.AssistantText, FinishReason: completion.FinishReason,
		Usage: completion.Usage, Completion: &completion,
		ErrorMessage: result.ErrorMessage, ErrorPayload: result.ErrorPayload,
	}, result.Err
}
