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
	MemberKey string `json:"memberKey"`
	TaskName  string `json:"taskName,omitempty"`
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
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
	PhaseSummarizing Phase = "summarizing"
	PhaseComplete    Phase = "complete"
	PhaseFailed      Phase = "failed"
)

type NextAction string

const (
	ActionRetryRouting        NextAction = "retry_routing"
	ActionContinueCoordinator NextAction = "continue_coordinator"
	ActionSummarize           NextAction = "summarize"
	ActionComplete            NextAction = "complete"
)

var (
	ErrToolRouteRequired     = errors.New("Team coordinator must route with a Team tool")
	ErrRoutingRetryExhausted = errors.New("Team coordinator failed to produce a valid route")
	ErrInvalidTransition     = errors.New("invalid Team coordinator transition")
)

// StateMachine captures the mode-specific orchestration invariants without
// knowing anything about model protocols, catalogs, streams or persistence.
// It is intentionally small so adapters can drive it from any provider.
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

// RejectPlainText enforces tool-first routing. After a team_invoke result or
// during fanout summary composition, ordinary coordinator text is a valid
// final answer.
func (m *StateMachine) RejectPlainText() (NextAction, error) {
	if m == nil {
		return "", ErrInvalidTransition
	}
	switch m.phase {
	case PhaseCoordinator, PhaseSummarizing:
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
	if dispatch.Kind != DispatchKindDirect && dispatch.Kind != DispatchKindFanout && dispatch.Kind != DispatchKindInvoke {
		return fmt.Errorf("%w: unknown dispatch kind %q", ErrInvalidTransition, dispatch.Kind)
	}
	if len(dispatch.Tasks) == 0 {
		return fmt.Errorf("%w: dispatch requires tasks", ErrInvalidTransition)
	}
	m.active = dispatch
	m.dispatchCount++
	m.phase = PhaseWaiting
	return nil
}

func (m *StateMachine) FinishDispatch(results []MemberResult) (NextAction, error) {
	if m == nil || m.phase != PhaseWaiting {
		return "", fmt.Errorf("%w: no active dispatch", ErrInvalidTransition)
	}
	active := m.active
	m.active = Dispatch{}
	switch active.Kind {
	case DispatchKindDirect:
		if len(results) == 1 && results[0].Succeeded() {
			m.phase = PhaseComplete
			return ActionComplete, nil
		}
		// A failed direct route is returned to the coordinator so it can choose
		// another member or explain the structured failure.
		m.phase = PhaseCoordinator
		return ActionContinueCoordinator, nil
	case DispatchKindFanout:
		m.phase = PhaseSummarizing
		return ActionSummarize, nil
	case DispatchKindInvoke:
		m.phase = PhaseCoordinator
		return ActionContinueCoordinator, nil
	default:
		m.phase = PhaseFailed
		return "", fmt.Errorf("%w: unknown active dispatch kind %q", ErrInvalidTransition, active.Kind)
	}
}
