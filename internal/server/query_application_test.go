package server

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/interaction"
	"agent-platform/internal/runtime/query"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
) // Test adapters exercise the Runtime application directly; they contain no
// admission, execution or recovery implementation.
func (s *Server) persistedRunLiveSeq(chatID string, runID string) int64 {
	return testQueryService(s).PersistedRunLiveSeq(chatID, runID)
}
func (s *Server) persistDeferredAwaitingToolAnswer(chatID string, runID string, awaitingID string, answer map[string]any, resolvedAt int64) error {
	return testQueryService(s).PersistDeferredAwaitingToolAnswer(chatID, runID, awaitingID, answer, resolvedAt)
}
func (s *Server) localRunExecutorParams(
	prepared preparedQuery,
	registered registeredQueryRun,
	eventBus *stream.RunEventBus,
) RunExecutorParams {
	return testQueryService(s).LocalRunExecutorParams(runtimePreparedQuery(prepared), runtimetypes.RegisteredRun(registered), eventBus)
}
func (s *Server) prepareQueryAdmissionRequest(
	ctx context.Context,
	req api.QueryRequest,
	requireMessage bool,
	locale string,
	resourceBaseURL string,
) (result queryAdmission, resultErr error) {
	return testQueryService(s).PrepareQueryAdmissionRequest(ctx, trustedQueryCommand(ctx, req), requireMessage, locale, resourceBaseURL)
}
func (s *Server) completeQueryPreparation(ctx context.Context, admission queryAdmission, release queryReleaseFunc) (preparedQuery, error) {
	p, e := testQueryService(s).CompleteQueryPreparation(ctx, admission, release)
	return proxyPreparedQuery(p), e
}
func (s *Server) validateQueryModelOptions(options *api.QueryModelOptions, agentDef catalog.AgentDefinition) error {
	return testQueryService(s).ValidateQueryModelOptions(options, agentDef)
}
func (s *Server) prepareBlockingQuery(ctx context.Context, req api.QueryRequest, locale, baseURL string) (preparedQuery, error) {
	p, e := testQueryService(s).PrepareBlockingQuery(ctx, trustedQueryCommand(ctx, req), locale, baseURL)
	return proxyPreparedQuery(p), e
}
func (s *Server) awaitingQueryGateError(chatID string, summary *chat.Summary) *statusError {
	return testQueryService(s).AwaitingQueryGateError(chatID, summary)
}
func (s *Server) registerRecoveredAwaitingRun(item chat.PendingAwaitingWithChat) (contracts.RecoveredAwaitingRun, error) {
	return testQueryService(s).RegisterRecoveredAwaitingRun(item)
}
func (s *Server) loadPersistedAwaitingStep(chatID string, awaitingID string) (*chat.PersistedAwaitingStep, error) {
	return testQueryService(s).LoadPersistedAwaitingStep(chatID, awaitingID)
}
func (s *Server) finishTerminalAwaiting(item chat.PendingAwaitingWithChat, answer map[string]any, resolvedAt int64) (contracts.AwaitingResolutionState, error) {
	return testQueryService(s).FinishTerminalAwaiting(item, answer, resolvedAt)
}
func (s *Server) freezeRunConnectors(prepared preparedQuery) error {
	return testQueryService(s).FreezeRunConnectors(runtimePreparedQuery(prepared))
}
func (s *Server) restoreRunConnectors(runID string, def *catalog.AgentDefinition) error {
	return testQueryService(s).RestoreRunConnectors(runID, def)
}
func (s *Server) runInteractionPolicies() interaction.Store {
	return testQueryService(s).RunInteractionPolicies()
}
func (s *Server) restoredInteractionPolicy(runID, mode string, query *chat.QueryLine) (*interaction.Config, error) {
	return testQueryService(s).RestoredInteractionPolicy(runID, mode, query)
}
func (s *Server) PrepareRunStart(ctx context.Context, request contracts.RunStartRequest) (contracts.RunStartPlan, error) {
	return testQueryService(s).PrepareRunStart(ctx, request)
}

func (s *Server) StartRun(ctx context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	return testQueryService(s).StartRun(ctx, request)
}
func (s *Server) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return testQueryService(s).GetRunStatus(runID)
}
func (s *Server) ExecuteQuery(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	return s.deps.Runtime.ExecuteQueryWithHooks(ctx, cmd, hooks)
}
func (s *Server) validateSubmitOwner(req api.SubmitRequest) *statusError {
	return testQueryService(s).ValidateSubmitOwner(req)
}

type queryAdmission = query.Admission

func proxyPreparedQuery(p runtimetypes.PreparedQuery) preparedQuery {
	out := preparedQuery{Req: queryRequestFromRuntime(p.Req), Summary: p.Summary, Created: p.Created, AgentDef: p.AgentDef, TeamSnapshot: p.TeamSnapshot, Session: p.Session, SystemInitLine: p.SystemInitLine, ResourceBaseURL: p.ResourceBaseURL, Release: p.Release, ContinueRun: p.ContinueRun, InitialSeq: p.InitialSeq, SyntheticBootstrap: p.SyntheticBootstrap}
	if p.Execution != nil {
		e := queryExecutionOptions(*p.Execution)
		out.Execution = &e
	}
	return out
}
