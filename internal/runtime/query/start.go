package query

import (
	"context"
	"log"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type queryHooksKey struct{}

func notifyInternalQueryRunStarted(ctx context.Context, start chat.RunStart) {
	hooks, _ := ctx.Value(queryHooksKey{}).(runtimetypes.QueryHooks)
	if hooks.OnRunStarted == nil {
		return
	}
	defer func() {
		if v := recover(); v != nil {
			log.Printf("[runtime] OnRunStarted panic recovered runID=%s err=%v", start.RunID, v)
		}
	}()
	hooks.OnRunStarted(start)
}
func queryContext(ctx context.Context, cmd runtimetypes.QueryCommand) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity := cmd.Identity
	if identity == nil && strings.TrimSpace(cmd.Caller.Subject) != "" {
		identity = &contracts.AuthIdentity{Subject: cmd.Caller.Subject, DeviceID: cmd.Caller.DeviceID, Scope: cmd.Caller.Scope}
	}
	if identity != nil {
		ctx = runtimetypes.WithIdentity(ctx, identity)
	}
	if cmd.ChatSource != "" {
		ctx = runtimetypes.WithChatSource(ctx, cmd.ChatSource)
	}
	return ctx
}
func (s *Service) PrepareBlockingQuery(ctx context.Context, cmd runtimetypes.QueryCommand, locale, baseURL string) (preparedQuery, error) {
	admission, err := s.PrepareQueryAdmissionRequest(ctx, cmd, true, locale, baseURL)
	if err != nil {
		return preparedQuery{}, err
	}
	return s.CompleteQueryPreparation(ctx, admission, nil)
}
func (s *Service) prepare(ctx context.Context, cmd runtimetypes.QueryCommand) (preparedQuery, error) {
	locale := strings.TrimSpace(cmd.Locale)
	if locale == "" {
		locale = i18n.DefaultLocale
	}
	cmd.Locale = locale
	var prepared preparedQuery
	var err error
	if cmd.SideQuery {
		var statusErr *statusError
		prepared, statusErr = s.prepareSideQuery(ctx, cmd)
		if statusErr != nil {
			err = statusErr
		}
	} else {
		prepared, err = s.PrepareBlockingQuery(ctx, cmd, locale, cmd.ResourceBaseURL)
	}
	if err != nil {
		return prepared, err
	}
	prepared.Session.WebClientTarget = contracts.WebClientTarget{SessionID: cmd.ClientTarget.SessionID, BoundaryKey: cmd.ClientTarget.BoundaryKey, Subject: cmd.ClientTarget.Subject, SurfaceID: cmd.ClientTarget.SurfaceID}
	return prepared, nil
}
func (s *Service) StartQuery(ctx context.Context, cmd runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	ctx = queryContext(ctx, cmd)
	prepared, err := s.prepare(ctx, cmd)
	if err != nil {
		return runtimetypes.RunHandle{}, err
	}
	registered, statusErr := s.RegisterPreparedQuery(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		return runtimetypes.RunHandle{}, statusErr
	}
	bus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
	if !ok {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.FinishRegisteredQuery(prepared, registered)
		return runtimetypes.RunHandle{}, &contracts.RunToolError{Code: "internal_error", Message: "run event bus unavailable"}
	}
	if session.IsProxyRoutedAgent(prepared.AgentDef) {
		wait := proxy.UpstreamTransport(prepared.AgentDef.ProxyConfig) == "sse" && strings.TrimSpace(cmd.ClientTarget.SessionID) == ""
		if err := s.deps.Proxy.Start(prepared, registered, bus, wait); err != nil {
			return runtimetypes.RunHandle{}, err
		}
	} else {
		s.startPreparedLocalRun(prepared, registered, bus)
	}
	owner := contracts.ResolveRunOwner(prepared.Session.RunOwner)
	return runtimetypes.RunHandle{SideQueryID: sideQueryID(prepared), RunID: prepared.Req.RunID, ChatID: prepared.Req.ChatID, AgentKey: owner.AgentKey, TeamID: owner.TeamID, StartedAt: registered.StartedAtMillis, Status: "running", Detached: true}, nil
}
func (s *Service) ExecuteQuery(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	ctx = context.WithValue(queryContext(ctx, cmd), queryHooksKey{}, hooks)
	prepared, err := s.prepare(ctx, cmd)
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	registered, statusErr := s.RegisterPreparedQuery(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		return runtimetypes.QueryResult{}, statusErr
	}
	if session.IsProxyRoutedAgent(prepared.AgentDef) {
		bus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
		if !ok {
			releaseQuery(prepared.Release)
			s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
			s.FinishRegisteredQuery(prepared, registered)
			return runtimetypes.QueryResult{}, &contracts.RunToolError{Code: "internal_error", Message: "run event bus unavailable"}
		}
		return s.deps.Proxy.Execute(prepared, registered, bus)
	}
	var fullText *queryFullTextBuilder
	var observe func(stream.EventData)
	if cmd.IncludeFullText {
		fullText = newQueryFullTextBuilder()
		observe = fullText.Observe
	}
	result, err := s.executePreparedLocalQuery(prepared, registered, nil, observe)
	out := runtimetypes.QueryResult{Usage: result.Usage, FinishReason: result.FinishReason, SideQueryID: sideQueryID(prepared), RunID: prepared.Req.RunID, ChatID: prepared.Req.ChatID, ErrorPayload: result.ErrorPayload, Completion: result.Completion, Content: result.AssistantText, ErrorMessage: result.ErrorMessage}
	if fullText != nil {
		out.FullText = fullText.Text(result.AssistantText)
	}
	return out, err
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func queryReferenceStatusError(status int, code, message string) *statusError {
	return &statusError{Status: status, Code: code, Message: message, Data: map[string]any{"error": map[string]any{"code": code, "message": message}}}
}

func sideQueryID(prepared preparedQuery) string {
	if prepared.Execution != nil {
		return prepared.Execution.BTWID
	}
	return ""
}
