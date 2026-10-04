package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	req, err := decodeQueryRequest(r)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	if req.Detached != nil && *req.Detached {
		writeStatusError(w, btwStatusError(http.StatusBadRequest, "detached_ws_required", "detached queries require the main WebSocket connection"))
		return
	}
	lane := strings.TrimSpace(req.Lane)
	if lane == "" {
		lane = "main"
	}
	r = r.WithContext(controlscope.WithContext(r.Context(), httpControlScope(r.Context(), lane)))
	switch lane {
	case "", "main":
		// Ordinary HTTP queries retain their existing admission and execution path.
	case "btw":
		command := trustedQueryCommand(r.Context(), req)
		command.SideQuery = true
		command.SideQueryID = req.BTWID
		command.Locale = requestLocale(r, i18n.DefaultLocale)
		command.ResourceBaseURL = requestBaseURL(r)
		s.writeRuntimeQueryResponse(w, r.Context(), command)

		return
	case "explain":
		writeStatusError(w, btwStatusError(http.StatusForbidden, "explain_ws_required", "explain requires the Desktop explain WebSocket connection"))
		return
	default:
		writeStatusError(w, btwStatusError(http.StatusBadRequest, "invalid_lane", "HTTP query lane must be main or btw"))
		return
	}
	if !isSyncQueryContext(r.Context()) && !isNonStreamingQuery(req) {
		s.handleRuntimeQueryAsync(w, r, req)
		return
	}
	command := trustedQueryCommand(r.Context(), req)
	command.Locale = requestLocale(r, i18n.DefaultLocale)
	command.ResourceBaseURL = requestBaseURL(r)
	command.ChatSource = chatSourceFromContext(r.Context())
	command.ClientTarget = runtimeClientTarget(webClientTargetFromHTTPRequest(r))
	s.writeRuntimeQueryNonStream(w, r.Context(), command)

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
		message := i18n.Translate(responseLocale(w), string(appErr.Code()), err.Error())
		writeJSON(w, status, api.Failure(status, message, appErr.Payload()))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) handleRuntimeQueryAsync(w http.ResponseWriter, r *http.Request, req api.QueryRequest) {
	command := trustedQueryCommand(r.Context(), req)
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
	if handle.SideQueryID != "" {
		w.Header().Set("X-Btw-Id", handle.SideQueryID)
		w.Header().Set("X-Run-Id", handle.RunID)
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

func isNonStreamingQuery(req api.QueryRequest) bool {
	return req.Stream != nil && !*req.Stream
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
	AssistantText   strings.Builder
	ModelTurnText   strings.Builder
	ModelTurnOpen   bool
	ModelTurnDirty  bool
	FinishReason    string
	Usage           chat.UsageData
	FullTextBuilder *queryFullTextBuilder
	ErrorMessage    string
	ErrorPayload    map[string]any
}

func newQueryEventCollector(includeFullText bool) *queryEventCollector {
	c := &queryEventCollector{}
	if includeFullText {
		c.FullTextBuilder = newQueryFullTextBuilder()
	}
	return c
}

func (c *queryEventCollector) Consume(event stream.EventData) {
	if c == nil {
		return
	}
	if event.Type == "run.activity" && isDiscardIncompleteModelTurnRecovery(event.Value("recovery")) {
		c.ModelTurnText.Reset()
		c.ModelTurnOpen = true
		c.ModelTurnDirty = false
		if c.FullTextBuilder != nil {
			c.FullTextBuilder.DiscardModelTurn(event.Value("recovery"))
		}
		return
	}
	if c.FullTextBuilder != nil {
		c.FullTextBuilder.Consume(event)
	}
	switch event.Type {
	case "llm.request":
		c.FinishModelTurn()
		c.ModelTurnOpen = true
		c.ModelTurnDirty = false
	case "content.delta":
		if delta := event.String("delta"); delta != "" {
			c.ModelTurnDirty = c.ModelTurnOpen
			c.ContentBuffer().WriteString(delta)
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			c.ModelTurnDirty = c.ModelTurnOpen
			buffer := c.ContentBuffer()
			buffer.Reset()
			buffer.WriteString(text)
		}
	case "content.end":
		if text := event.String("text"); text != "" && c.ContentBuffer().Len() == 0 {
			c.ModelTurnDirty = c.ModelTurnOpen
			c.ContentBuffer().WriteString(text)
		}
	case "usage.snapshot":
		c.ConsumeUsage(event)
	case "run.complete":
		c.FinishModelTurn()
		c.FinishReason = "complete"
		c.ConsumeUsage(event)
	case "run.cancel":
		c.FinishModelTurn()
		c.FinishReason = "cancel"
		c.ConsumeUsage(event)
	case "run.error":
		c.FinishModelTurn()
		c.FinishReason = "error"
		if message := queryEventErrorMessage(event); message != "" {
			c.ErrorMessage = message
		}
		if payload := queryEventErrorPayload(event); len(payload) > 0 {
			c.ErrorPayload = payload
		}
		c.ConsumeUsage(event)
	}
}

func (c *queryEventCollector) ContentBuffer() *strings.Builder {
	if c.ModelTurnOpen {
		return &c.ModelTurnText
	}
	return &c.AssistantText
}

func (c *queryEventCollector) FinishModelTurn() {
	if !c.ModelTurnOpen {
		return
	}
	if c.ModelTurnDirty {
		c.AssistantText.Reset()
		c.AssistantText.WriteString(c.ModelTurnText.String())
	}
	c.ModelTurnText.Reset()
	c.ModelTurnOpen = false
	c.ModelTurnDirty = false
}

func (c *queryEventCollector) Result() queryRunResult {
	if c == nil {
		return queryRunResult{FinishReason: "complete"}
	}
	finishReason := strings.TrimSpace(c.FinishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	assistantText := c.AssistantText.String()
	if c.ModelTurnOpen && c.ModelTurnDirty {
		assistantText = c.ModelTurnText.String()
	}
	return queryRunResult{
		AssistantText: assistantText,
		FinishReason:  finishReason,
		Usage:         c.Usage,
		FullText:      c.FullText(assistantText),
		ErrorMessage:  c.ErrorMessage,
		ErrorPayload:  cloneQueryErrorPayload(c.ErrorPayload),
	}
}

func (c *queryEventCollector) FullText(content string) string {
	if c == nil || c.FullTextBuilder == nil {
		return ""
	}
	return c.FullTextBuilder.Text(content)
}

// Observe retains internal recovery signals; public EventBus events alone
// cannot reconstruct discarded model attempts for a blocking fullText result.

func (c *queryEventCollector) ConsumeUsage(event stream.EventData) {
	if c == nil || event.Payload == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		mergeUsageMapIntoRunData(&c.Usage, run)
		return
	}
	mergeUsageMapIntoRunData(&c.Usage, usage)
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
	queryResponse := queryResponseFromResult(prepared.Req, result)
	execution := prepared.Execution
	if execution == nil || strings.TrimSpace(execution.BTWID) == "" {
		return queryResponse
	}
	return api.BTWResponse{
		BTWID:        execution.BTWID,
		ParentChatID: execution.ParentChatID,
		RunID:        prepared.Req.RunID,
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

func (s *Server) writeRuntimeQueryResponse(w http.ResponseWriter, ctx context.Context, cmd runtimetypes.QueryCommand) {
	if cmd.Stream != nil && !*cmd.Stream {
		s.writeRuntimeQueryNonStream(w, ctx, cmd)
	} else {
		s.writeRuntimeQueryStream(w, ctx, cmd)
	}
}
func (s *Server) writeRuntimeQueryNonStream(w http.ResponseWriter, ctx context.Context, cmd runtimetypes.QueryCommand) {
	result, err := s.deps.Runtime.ExecuteQueryWithHooks(ctx, cmd, runtimetypes.QueryHooks{})
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	mapped := queryRunResult{Usage: result.Usage, FinishReason: result.FinishReason, AssistantText: result.Content, FullText: result.FullText, Completion: result.Completion, ErrorMessage: result.ErrorMessage, ErrorPayload: result.ErrorPayload}
	if result.Completion != nil {
		mapped.FinishReason = result.Completion.FinishReason
		mapped.Usage = result.Completion.Usage
	}
	if queryRunFailed(mapped) {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, queryRunErrorMessage(mapped), queryRunErrorPayload(mapped)))
		return
	}
	req := queryRequestFromRuntime(cmd)
	req.RunID = result.RunID
	p := preparedQuery{Req: req}
	if result.SideQueryID != "" {
		w.Header().Set("X-Btw-Id", result.SideQueryID)
		w.Header().Set("X-Run-Id", result.RunID)
		p.Execution = &queryExecutionOptions{BTWID: result.SideQueryID, ParentChatID: result.ChatID}
	}
	writeJSON(w, http.StatusOK, api.Success(queryResponsePayload(p, mapped)))
}
