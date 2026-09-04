package team

import (
	"errors"
	"fmt"
)

type Phase string

const (
	PhaseRouting     Phase = "routing"
	PhaseWaiting     Phase = "waiting_members"
	PhaseCoordinator Phase = "coordinator"
	PhaseComplete    Phase = "complete"
	PhaseFailed      Phase = "failed"
)

type NextAction string

const (
	ActionRetryRouting        NextAction = "retry_routing"
	ActionContinueCoordinator NextAction = "continue_coordinator"
	ActionComplete            NextAction = "complete"
)

var (
	ErrToolRouteRequired     = errors.New("Team coordinator must delegate with agent_delegate")
	ErrRoutingRetryExhausted = errors.New("TEAM coordinator did not produce a valid agent_delegate call after one correction")
	ErrInvalidTransition     = errors.New("invalid Team coordinator transition")
)

type StateMachine struct {
	phase          Phase
	routingRetries int
	dispatchCount  int
}

func NewStateMachine() *StateMachine {
	return &StateMachine{phase: PhaseRouting}
}

func (m *StateMachine) Phase() Phase {
	if m == nil || m.phase == "" {
		return PhaseRouting
	}
	return m.phase
}

func (m *StateMachine) DispatchCount() int {
	if m == nil {
		return 0
	}
	return m.dispatchCount
}

func (m *StateMachine) RequiresDelegation() bool {
	return m != nil && m.Phase() == PhaseRouting
}

func (m *StateMachine) RejectPlainText() (NextAction, error) {
	if m == nil {
		return "", ErrInvalidTransition
	}
	switch m.phase {
	case PhaseCoordinator:
		m.phase = PhaseComplete
		return ActionComplete, nil
	case PhaseRouting:
		if m.routingRetries < MaxRoutingRetries {
			m.routingRetries++
			return ActionRetryRouting, ErrToolRouteRequired
		}
		m.phase = PhaseFailed
		return "", ErrRoutingRetryExhausted
	default:
		return "", fmt.Errorf("%w: cannot accept coordinator text in phase %s", ErrInvalidTransition, m.phase)
	}
}

func (m *StateMachine) BeginDispatch(dispatch Dispatch) error {
	if m == nil {
		return ErrInvalidTransition
	}
	if m.phase != PhaseRouting && m.phase != PhaseCoordinator {
		return fmt.Errorf("%w: cannot dispatch in phase %s", ErrInvalidTransition, m.phase)
	}
	if len(dispatch.Tasks) == 0 {
		return fmt.Errorf("%w: dispatch requires tasks", ErrInvalidTransition)
	}
	m.dispatchCount++
	m.phase = PhaseWaiting
	return nil
}

func (m *StateMachine) FinishDispatch() (NextAction, error) {
	if m == nil || m.phase != PhaseWaiting {
		return "", fmt.Errorf("%w: no active dispatch", ErrInvalidTransition)
	}
	// Member success or failure is coordinator input, not a state-machine
	// branch. Either result returns control so the coordinator can retry,
	// delegate another batch, or synthesize the final answer.
	m.phase = PhaseCoordinator
	return ActionContinueCoordinator, nil
}
