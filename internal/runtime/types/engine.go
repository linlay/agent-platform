package types

import (
	"context"

	"agent-platform/internal/contracts"
)

// Engine starts one model stream from an internal command and frozen session.
type Engine interface {
	Stream(context.Context, QueryCommand, contracts.QuerySession) (contracts.AgentStream, error)
}
