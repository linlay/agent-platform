package team

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/api"
)

type DispatchRequest struct {
	TeamID          string
	RunID           string
	ChatID          string
	ToolID          string
	OriginalMessage string
	References      []api.Reference
	Dispatch        Dispatch
}

type MemberResult struct {
	AgentKey string `json:"agentKey"`
	TaskName string `json:"taskName,omitempty"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r MemberResult) Succeeded() bool {
	return strings.TrimSpace(r.Error) == ""
}

// Runtime is implemented by the runtime adapter outside this package. The
// Team package owns routing policy and state transitions; the adapter owns
// catalog/session resolution, member streams, persistence and event routing.
type Runtime interface {
	ExecuteDispatch(ctx context.Context, request DispatchRequest) ([]MemberResult, error)
}

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
	ErrRoutingRetryExhausted = errors.New("Team coordinator failed to produce a valid delegation")
	ErrInvalidTransition     = errors.New("invalid Team coordinator transition")
)

type StateMachine struct {
	phase          Phase
	routingRetries int
	dispatchCount  int
	active         Dispatch
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
	m.active = dispatch
	m.dispatchCount++
	m.phase = PhaseWaiting
	return nil
}

func (m *StateMachine) FinishDispatch(_ []MemberResult) (NextAction, error) {
	if m == nil || m.phase != PhaseWaiting {
		return "", fmt.Errorf("%w: no active dispatch", ErrInvalidTransition)
	}
	m.active = Dispatch{}
	m.phase = PhaseCoordinator
	return ActionContinueCoordinator, nil
}
