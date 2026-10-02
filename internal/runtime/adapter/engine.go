// Package adapter maps legacy executor contracts to internal runtime commands.
// It contains no query admission, run lifecycle or transport execution.
package adapter

import (
	"context"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type Engine struct{ contracts.AgentEngine }

func (e Engine) Stream(ctx context.Context, command runtimetypes.QueryCommand, session contracts.QuerySession) (contracts.AgentStream, error) {
	return e.AgentEngine.Stream(ctx, QueryRequest(command), session)
}

type Profiles struct {
	Builder contracts.SystemInitBuilder
	Tools   contracts.ToolExecutor
}

func (p Profiles) Profiles(command runtimetypes.QueryCommand, input contracts.QuerySession) ([]contracts.SystemInitProfile, error) {
	if p.Builder == nil || p.Tools == nil {
		return nil, nil
	}
	return p.Builder.BuildSystemInitProfiles(contracts.SystemInitBuildInput{Request: QueryRequest(command), Session: input, ToolDefinitions: p.Tools.Definitions()})
}

func (e Engine) BindSteerPreparer(session contracts.QuerySession, control *contracts.RunControl) error {
	if preparer, ok := e.AgentEngine.(contracts.RunSteerPreparer); ok {
		return preparer.BindSteerPreparer(session, control)
	}
	return nil
}
