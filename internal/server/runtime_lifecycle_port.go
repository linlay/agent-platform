package server

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	runtimetypes "agent-platform/internal/runtime/types"
)

// Root Proxy drivers retain wire ownership until R18; registration and pending
// reconciliation use the same Runtime implementation as Native runs.
func (s *Server) registerQueryRun(ctx context.Context, p preparedQuery) (registeredQueryRun, *statusError) {
	r, e := s.deps.Runtime.RegisterPreparedQuery(ctx, runtimePreparedQuery(p))
	return registeredQueryRun(r), e
}
func (s *Server) finishRegisteredQueryRun(p preparedQuery, r registeredQueryRun) {
	s.deps.Runtime.FinishRegisteredQuery(runtimePreparedQuery(p), runtimetypes.RegisteredRun(r))
}
func (s *Server) validateRunOwner(runID, agentKey, teamID string) *statusError {
	return s.deps.Runtime.ValidateRunOwner(runID, agentKey, teamID)
}
func (s *Server) validPendingAwaitingInfo(chatID string, pending *chat.PendingAwaiting) (*api.ChatErrorInfo, error) {
	return s.deps.Runtime.PendingAwaitingInfo(chatID, pending)
}
func runtimePreparedQuery(p preparedQuery) runtimetypes.PreparedQuery {
	out := runtimetypes.PreparedQuery{Req: queryCommandFromAPI(p.Req), Summary: p.Summary, Created: p.Created, AgentDef: p.AgentDef, TeamSnapshot: p.TeamSnapshot, Session: p.Session, SystemInitLine: p.SystemInitLine, ResourceBaseURL: p.ResourceBaseURL, Release: p.Release, ContinueRun: p.ContinueRun, InitialSeq: p.InitialSeq, SyntheticBootstrap: p.SyntheticBootstrap}
	if p.Execution != nil {
		e := runtimetypes.QueryExecutionOptions(*p.Execution)
		out.Execution = &e
	}
	return out
}
