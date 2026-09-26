package types

import (
	"context"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

type PreparedQuery struct {
	Req                QueryCommand
	Summary            chat.Summary
	Created            bool
	AgentDef           catalog.AgentDefinition
	TeamSnapshot       *catalog.TeamSnapshot
	Session            contracts.QuerySession
	MemoryUsageSummary *queryinput.MemoryUsageSummary
	SystemInitLine     *chat.QueryLineSystem
	ResourceBaseURL    string
	Release            func()
	ContinueRun        bool
	InitialSeq         int64
	SyntheticBootstrap *stream.SyntheticQuery
	Execution          *QueryExecutionOptions
}

type QueryExecutionOptions struct {
	StepLineStore   chat.StepLineStore
	CompletionStore chat.Store
	HiddenRun       bool
	QueryMetadata   map[string]any
	BTWID           string
	ParentChatID    string
}

type RegisteredRun struct {
	RunCtx          context.Context
	Control         *contracts.RunControl
	Managed         bool
	StartedAtMillis int64
}
