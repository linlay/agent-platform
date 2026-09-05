package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Server) StartQueryRuntime(ctx context.Context, command runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if subject := strings.TrimSpace(command.Caller.Subject); subject != "" {
		ctx = WithPrincipal(ctx, &Principal{Subject: subject})
	}
	req := queryRequestFromRuntime(command)
	if req.ChatSource != "" {
		ctx = withChatSourceContext(ctx, req.ChatSource)
	}
	locale := strings.TrimSpace(command.Locale)
	if locale == "" {
		locale = i18n.DefaultLocale
	}
	admission, err := s.prepareQueryAdmissionRequest(ctx, req, true, locale, command.ResourceBaseURL)
	if err != nil {
		return runtimetypes.RunHandle{}, err
	}
	prepared, err := s.completeQueryPreparation(ctx, admission, nil)
	if err != nil {
		return runtimetypes.RunHandle{}, err
	}
	prepared.session.WebClientTarget = contracts.WebClientTarget{
		SessionID: command.ClientTarget.SessionID, BoundaryKey: command.ClientTarget.BoundaryKey,
		Subject: command.ClientTarget.Subject, SurfaceID: command.ClientTarget.SurfaceID,
	}
	registered, statusErr := s.registerQueryRun(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		return runtimetypes.RunHandle{}, statusErr
	}
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		return runtimetypes.RunHandle{}, &contracts.RunToolError{Code: "internal_error", Message: "run event bus unavailable"}
	}
	if isProxyRoutedAgent(prepared.agentDef) {
		if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "sse" && strings.TrimSpace(command.ClientTarget.SessionID) == "" {
			if err := s.startPreparedProxyRunAndWait(prepared, registered, eventBus); err != nil {
				return runtimetypes.RunHandle{}, err
			}
		} else {
			s.startPreparedProxyRun(prepared, registered, eventBus)
		}
	} else {
		s.startPreparedLocalRun(prepared, registered, eventBus, PrincipalFromContext(ctx))
	}
	owner := contracts.ResolveRunOwner(prepared.session.RunOwner)
	return runtimetypes.RunHandle{
		RunID: prepared.req.RunID, ChatID: prepared.req.ChatID, AgentKey: owner.AgentKey,
		TeamID: owner.TeamID, StartedAt: registered.StartedAtMillis, Status: "running", Detached: true,
	}, nil
}

// ExecuteQuery adapts the transport-neutral Runtime command to the legacy
// query pipeline while that pipeline is being extracted from server. New
// in-process callers depend on Runtime; this adapter is intentionally the
// only reverse seam during the migration.
func (s *Server) ExecuteQuery(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if subject := strings.TrimSpace(cmd.Caller.Subject); subject != "" {
		ctx = WithPrincipal(ctx, &Principal{Subject: subject})
	}
	req := queryRequestFromRuntime(cmd)
	result, err := s.ExecuteInternalQueryResult(ctx, req, InternalQueryHooks{OnRunStarted: hooks.OnRunStarted})
	queryResult := runtimetypes.QueryResult{
		Completion:   result.Completion,
		ErrorMessage: result.ErrorMessage,
	}
	if err != nil {
		return queryResult, err
	}
	if result.StatusCode != http.StatusOK {
		return queryResult, fmt.Errorf("query failed with status %d: %s", result.StatusCode, summarizeRuntimeQueryBody(result.Body))
	}
	return queryResult, nil
}

func (s *Server) SubmitRuntime(_ context.Context, command runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	req := api.SubmitRequest{
		ChatID: command.ChatID, RunID: command.RunID, AgentKey: command.AgentKey, TeamID: command.TeamID,
		AwaitingID: command.AwaitingID, SubmitID: command.SubmitID, Locale: command.Locale,
		Params: api.SubmitParams(command.Params), ContinuationRunID: command.ContinuationRunID,
		ContinuationState: command.ContinuationState,
	}
	req = s.normalizeActiveSubmitRun(req)
	if statusErr := s.validateSubmitOwner(req); statusErr != nil {
		return runtimetypes.SubmitResult{}, statusErr
	}
	if response, statusErr, ok := s.forwardProxySubmit(req); ok {
		if statusErr != nil {
			return runtimetypes.SubmitResult{}, statusErr
		}
		return submitResultToRuntime(response), nil
	}
	response, _, _, err := s.resolveSubmit(req)
	if err != nil {
		return runtimetypes.SubmitResult{}, err
	}
	return submitResultToRuntime(response), nil
}

func (s *Server) SteerRuntime(_ context.Context, command runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	req := api.SteerRequest{
		RequestID: command.RequestID, ChatID: command.ChatID, RunID: command.RunID,
		SteerID: command.SteerID, AgentKey: command.AgentKey, TeamID: command.TeamID, Message: command.Message,
	}
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return runtimetypes.SteerResult{}, statusErr
	}
	if response, statusErr, ok := s.forwardProxySteer(req); ok {
		if statusErr != nil {
			return runtimetypes.SteerResult{}, statusErr
		}
		return runtimetypes.SteerResult{Accepted: response.Accepted, Status: response.Status, RunID: response.RunID, SteerID: response.SteerID, Detail: response.Detail}, nil
	}
	ack := s.deps.Runs.Steer(req)
	return runtimetypes.SteerResult{Accepted: ack.Accepted, Status: ack.Status, RunID: req.RunID, SteerID: ack.SteerID, Detail: ack.Detail}, nil
}

func (s *Server) InterruptRuntime(_ context.Context, command runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	req := api.InterruptRequest{
		RequestID: command.RequestID, ChatID: command.ChatID, RunID: command.RunID,
		AgentKey: command.AgentKey, TeamID: command.TeamID, Message: command.Message,
		InterruptSource: command.Source, InterruptReason: command.Reason, InterruptDetail: command.Detail,
	}
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return runtimetypes.InterruptResult{}, statusErr
	}
	if response, statusErr, forwarded := s.forwardProxyInterrupt(req); forwarded {
		if statusErr != nil {
			return runtimetypes.InterruptResult{}, statusErr
		}
		s.deps.Runs.Interrupt(runtimeLocalInterruptRequest(command, req))
		if command.Caller.Scope == "server" {
			s.deps.Runs.Finish(req.RunID)
		}
		return runtimetypes.InterruptResult{Accepted: response.Accepted, Status: response.Status, RunID: response.RunID, Detail: response.Detail}, nil
	}
	ack := s.deps.Runs.Interrupt(runtimeLocalInterruptRequest(command, req))
	if command.Caller.Scope == "server" {
		s.deps.Runs.Finish(req.RunID)
	}
	return runtimetypes.InterruptResult{Accepted: ack.Accepted, Status: ack.Status, RunID: req.RunID, Detail: ack.Detail}, nil
}

func runtimeLocalInterruptRequest(command runtimetypes.InterruptCommand, req api.InterruptRequest) api.InterruptRequest {
	if command.Caller.Scope == "server" {
		return interruptRequestWithCause(req, command.Source, command.Reason, command.Detail)
	}
	return httpAPIUserInterruptRequest(req)
}

func (s *Server) SetAccessLevelRuntime(_ context.Context, command runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	response, statusErr := s.updateAccessLevel(api.AccessLevelRequest{
		RequestID: command.RequestID, RunID: command.RunID, AgentKey: command.AgentKey,
		TeamID: command.TeamID, AccessLevel: command.AccessLevel, Reason: command.Reason,
	})
	if statusErr != nil {
		return runtimetypes.AccessLevelResult{}, statusErr
	}
	return runtimetypes.AccessLevelResult{
		Accepted: response.Accepted, Status: response.Status, RunID: response.RunID,
		PreviousAccessLevel: response.PreviousAccessLevel, AccessLevel: response.AccessLevel,
		Version: response.Version, Detail: response.Detail,
	}, nil
}

func submitResultToRuntime(response api.SubmitResponse) runtimetypes.SubmitResult {
	return runtimetypes.SubmitResult{
		Accepted: response.Accepted, Status: response.Status, ChatID: response.ChatID, RunID: response.RunID,
		AwaitingID: response.AwaitingID, SubmitID: response.SubmitID, Continued: response.Continued,
		ErrorCode: response.ErrorCode, Detail: response.Detail,
	}
}

func queryRequestFromRuntime(cmd runtimetypes.QueryCommand) api.QueryRequest {
	references := apiReferencesFromRuntime(cmd.References)
	var scene *api.Scene
	if cmd.Scene != nil {
		scene = &api.Scene{URL: cmd.Scene.URL, Title: cmd.Scene.Title}
	}
	var model *api.QueryModelOptions
	if cmd.Model != nil {
		model = &api.QueryModelOptions{Key: cmd.Model.Key, ModelID: cmd.Model.ModelID, ReasoningEffort: cmd.Model.ReasoningEffort, ServiceTier: cmd.Model.ServiceTier}
	}
	return api.QueryRequest{
		RequestID: cmd.RequestID, RunID: cmd.RunID, ChatID: cmd.ChatID, AgentKey: cmd.AgentKey, TeamID: cmd.TeamID,
		Role: cmd.Role, Hidden: cmd.Hidden, Message: cmd.Message, SourceUser: cmd.SourceUser, References: references,
		Params: contracts.CloneMap(cmd.Params), Scene: scene, Stream: cmd.Stream, IncludeUsage: cmd.IncludeUsage,
		IncludeFullText: cmd.IncludeFullText, PlanningMode: cmd.PlanningMode, EditingMode: cmd.EditingMode,
		MustUseSkills: append([]string(nil), cmd.MustUseSkills...), AccessLevel: cmd.AccessLevel, Model: model,
		SyntheticQueryBootstrapped: cmd.SyntheticQueryBootstrapped, ChatSource: cmd.ChatSource,
		TrustedQueryMetadata: contracts.CloneMap(cmd.TrustedQueryMetadata),
	}
}

func queryCommandFromAPI(req api.QueryRequest) runtimetypes.QueryCommand {
	var scene *runtimetypes.Scene
	if req.Scene != nil {
		scene = &runtimetypes.Scene{URL: req.Scene.URL, Title: req.Scene.Title}
	}
	var model *runtimetypes.QueryModelOptions
	if req.Model != nil {
		model = &runtimetypes.QueryModelOptions{Key: req.Model.Key, ModelID: req.Model.ModelID, ReasoningEffort: req.Model.ReasoningEffort, ServiceTier: req.Model.ServiceTier}
	}
	return runtimetypes.QueryCommand{
		RequestID: req.RequestID, RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID,
		Role: req.Role, Hidden: req.Hidden, Message: req.Message, SourceUser: req.SourceUser,
		References: runtimeReferencesFromAPI(req.References), Params: contracts.CloneMap(req.Params), Scene: scene,
		Stream: req.Stream, IncludeUsage: req.IncludeUsage, IncludeFullText: req.IncludeFullText,
		PlanningMode: req.PlanningMode, EditingMode: req.EditingMode, MustUseSkills: append([]string(nil), req.MustUseSkills...),
		AccessLevel: req.AccessLevel, Model: model, SyntheticQueryBootstrapped: req.SyntheticQueryBootstrapped,
		ChatSource: req.ChatSource, TrustedQueryMetadata: contracts.CloneMap(req.TrustedQueryMetadata),
	}
}

func runtimeClientTarget(target contracts.WebClientTarget) runtimetypes.ClientTarget {
	return runtimetypes.ClientTarget{
		SessionID: target.SessionID, BoundaryKey: target.BoundaryKey,
		Subject: target.Subject, SurfaceID: target.SurfaceID,
	}
}

func runtimeReferencesFromAPI(references []api.Reference) []runtimetypes.Reference {
	converted := make([]runtimetypes.Reference, len(references))
	for index, reference := range references {
		converted[index] = runtimetypes.Reference{
			ID: reference.ID, Type: reference.Type, Name: reference.Name, Path: reference.Path,
			MimeType: reference.MimeType, SizeBytes: reference.SizeBytes, URL: reference.URL,
			SHA256: reference.SHA256, Meta: contracts.CloneMap(reference.Meta),
		}
	}
	return converted
}

func apiReferencesFromRuntime(references []runtimetypes.Reference) []api.Reference {
	converted := make([]api.Reference, len(references))
	for index, reference := range references {
		converted[index] = api.Reference{
			ID: reference.ID, Type: reference.Type, Name: reference.Name, Path: reference.Path,
			MimeType: reference.MimeType, SizeBytes: reference.SizeBytes, URL: reference.URL,
			SHA256: reference.SHA256, Meta: contracts.CloneMap(reference.Meta),
		}
	}
	return converted
}

func summarizeRuntimeQueryBody(body string) string {
	body = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	if body == "" {
		return "<empty body>"
	}
	const maxLen = 240
	if len(body) > maxLen {
		return body[:maxLen] + "..."
	}
	return body
}
