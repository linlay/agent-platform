package contracts

import (
	"agent-platform/internal/api"
	"context"
)

// SummaryAgentEngine opens exactly one isolated, tool-free summary call.
type SummaryAgentEngine interface {
	StreamSummary(context.Context, api.QueryRequest, QuerySession, string, int) (AgentStream, error)
}

type ContextEstimator interface {
	EstimateContext(context.Context, api.QueryRequest, QuerySession) (int, error)
}
