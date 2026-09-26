package orchestration

import (
	"errors"
	"io"

	"agent-platform/internal/contracts"
)

// DeltaHandler is the execution port implemented by the orchestration
// environment. It keeps transport and persistence concerns outside the delta
// interpreter while allowing ordinary and Team dispatch to share one loop.
type DeltaHandler interface {
	Emit(contracts.AgentDelta)
	HandleSubAgents(contracts.AgentStream, contracts.DeltaInvokeSubAgents) error
	HandleTeamDispatch(contracts.AgentStream, contracts.DeltaTeamDispatch) (terminal bool, err error)
}

type Result struct {
	StreamFailed      bool
	StreamInterrupted bool
}

// Run interprets orchestration deltas until the main stream reaches a
// terminal condition. Child-session construction and result injection are
// supplied through DeltaHandler ports.
func Run(mainStream contracts.AgentStream, handler DeltaHandler) (Result, error) {
	for {
		delta, err := mainStream.Next()
		if errors.Is(err, io.EOF) {
			return Result{}, nil
		}
		if contracts.IsRunInterrupted(err) {
			return Result{StreamInterrupted: true}, nil
		}
		if err != nil {
			return Result{StreamFailed: true}, err
		}
		switch value := delta.(type) {
		case contracts.DeltaInvokeSubAgents:
			if err := handler.HandleSubAgents(mainStream, value); err != nil {
				return Result{StreamFailed: true}, err
			}
		case contracts.DeltaTeamDispatch:
			terminal, err := handler.HandleTeamDispatch(mainStream, value)
			if err != nil {
				return Result{StreamFailed: true}, err
			}
			if terminal {
				return Result{}, nil
			}
		default:
			handler.Emit(delta)
		}
	}
}
