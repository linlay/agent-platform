package team

import (
	"errors"
	"testing"
)

func TestStateMachineRequiresDelegationAndAllowsOneCorrection(t *testing.T) {
	machine := NewStateMachine()
	action, err := machine.RejectPlainText()
	if action != ActionRetryRouting || !errors.Is(err, ErrToolRouteRequired) || machine.Phase() != PhaseRouting {
		t.Fatalf("first text action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	action, err = machine.RejectPlainText()
	if action != "" || !errors.Is(err, ErrRoutingRetryExhausted) || machine.Phase() != PhaseFailed {
		t.Fatalf("second text action=%q err=%v phase=%q", action, err, machine.Phase())
	}
}

func TestStateMachineAlwaysReturnsDelegationResultsToCoordinator(t *testing.T) {
	machine := NewStateMachine()
	dispatch := Dispatch{Tasks: []TaskSpec{{AgentKey: "writer"}}}
	if err := machine.BeginDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	action, err := machine.FinishDispatch([]MemberResult{{AgentKey: "writer", Content: "done"}})
	if err != nil || action != ActionContinueCoordinator || machine.Phase() != PhaseCoordinator || machine.DispatchCount() != 1 {
		t.Fatalf("action=%q err=%v phase=%q count=%d", action, err, machine.Phase(), machine.DispatchCount())
	}
	action, err = machine.RejectPlainText()
	if err != nil || action != ActionComplete || machine.Phase() != PhaseComplete {
		t.Fatalf("final action=%q err=%v phase=%q", action, err, machine.Phase())
	}
}

func TestStateMachineAllowsAnotherPlanDrivenDelegation(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "writer"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.FinishDispatch([]MemberResult{{AgentKey: "writer", Error: "unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "reviewer"}}}); err != nil {
		t.Fatalf("second delegation failed: %v", err)
	}
}
