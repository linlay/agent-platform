package team

import (
	"errors"
	"testing"
)

func TestStateMachineRequiresDelegationAndAllowsOneCorrection(t *testing.T) {
	machine := NewStateMachine()
	if !machine.RequiresDelegation() {
		t.Fatal("new state machine must require an initial delegation")
	}
	action, err := machine.RejectPlainText()
	if action != ActionRetryRouting || !errors.Is(err, ErrToolRouteRequired) || machine.Phase() != PhaseRouting {
		t.Fatalf("first text action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	action, err = machine.RejectPlainText()
	if action != "" || !errors.Is(err, ErrRoutingRetryExhausted) || machine.Phase() != PhaseFailed {
		t.Fatalf("second text action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	if machine.RequiresDelegation() {
		t.Fatal("failed state machine must not report a routable phase")
	}
}

func TestStateMachineAlwaysReturnsDelegationResultsToCoordinator(t *testing.T) {
	machine := NewStateMachine()
	dispatch := Dispatch{Tasks: []TaskSpec{{AgentKey: "writer"}}}
	if err := machine.BeginDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	if machine.RequiresDelegation() {
		t.Fatal("waiting state machine must not require another delegation")
	}
	action, err := machine.FinishDispatch()
	if err != nil || action != ActionContinueCoordinator || machine.Phase() != PhaseCoordinator {
		t.Fatalf("action=%q err=%v phase=%q", action, err, machine.Phase())
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
	if _, err := machine.FinishDispatch(); err != nil {
		t.Fatal(err)
	}
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "reviewer"}}}); err != nil {
		t.Fatalf("second delegation failed: %v", err)
	}
}

func TestStateMachineRejectsInvalidTransitions(t *testing.T) {
	machine := NewStateMachine()
	if _, err := machine.FinishDispatch(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("finish without dispatch error=%v", err)
	}
	if err := machine.BeginDispatch(Dispatch{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("empty dispatch error=%v", err)
	}
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "writer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "reviewer"}}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("dispatch while waiting error=%v", err)
	}
	if _, err := machine.RejectPlainText(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("plain text while waiting error=%v", err)
	}
	if _, err := machine.FinishDispatch(); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.RejectPlainText(); err != nil {
		t.Fatal(err)
	}
	if err := machine.BeginDispatch(Dispatch{Tasks: []TaskSpec{{AgentKey: "writer"}}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("dispatch after completion error=%v", err)
	}
}
