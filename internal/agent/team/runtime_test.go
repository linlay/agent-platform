package team

import (
	"errors"
	"testing"
)

func TestStateMachineRequiresToolRouteAndAllowsOneCorrection(t *testing.T) {
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

func TestStateMachineDirectSuccessIsTerminal(t *testing.T) {
	machine := NewStateMachine()
	dispatch := Dispatch{Kind: DispatchKindDirect, Tasks: []TaskSpec{{MemberKey: "writer"}}}
	if err := machine.BeginDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	action, err := machine.FinishDispatch([]MemberResult{{MemberKey: "writer", Content: "done"}})
	if err != nil || action != ActionComplete || machine.Phase() != PhaseComplete || machine.DispatchCount() != 1 {
		t.Fatalf("action=%q err=%v phase=%q count=%d", action, err, machine.Phase(), machine.DispatchCount())
	}
}

func TestStateMachineFailedDirectCanReroute(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.BeginDispatch(Dispatch{Kind: DispatchKindDirect, Tasks: []TaskSpec{{MemberKey: "writer"}}}); err != nil {
		t.Fatal(err)
	}
	action, err := machine.FinishDispatch([]MemberResult{{MemberKey: "writer", Error: "unavailable"}})
	if err != nil || action != ActionContinueCoordinator || machine.Phase() != PhaseCoordinator {
		t.Fatalf("action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	if err := machine.BeginDispatch(Dispatch{Kind: DispatchKindDirect, Tasks: []TaskSpec{{MemberKey: "reviewer"}}}); err != nil {
		t.Fatalf("reroute failed: %v", err)
	}
}

func TestStateMachineFanoutRequiresSummary(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.BeginDispatch(Dispatch{Kind: DispatchKindFanout, Tasks: []TaskSpec{{MemberKey: "a"}, {MemberKey: "b"}}}); err != nil {
		t.Fatal(err)
	}
	action, err := machine.FinishDispatch([]MemberResult{{MemberKey: "a", Content: "one"}, {MemberKey: "b", Error: "failed"}})
	if err != nil || action != ActionSummarize || machine.Phase() != PhaseSummarizing {
		t.Fatalf("action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	action, err = machine.RejectPlainText()
	if err != nil || action != ActionComplete || machine.Phase() != PhaseComplete {
		t.Fatalf("summary action=%q err=%v phase=%q", action, err, machine.Phase())
	}
}

func TestStateMachineInvokeCanContinueOrFinish(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.BeginDispatch(Dispatch{Kind: DispatchKindInvoke, Tasks: []TaskSpec{{MemberKey: "a", Task: "draft"}}}); err != nil {
		t.Fatal(err)
	}
	action, err := machine.FinishDispatch([]MemberResult{{MemberKey: "a", Content: "drafted"}})
	if err != nil || action != ActionContinueCoordinator || machine.Phase() != PhaseCoordinator {
		t.Fatalf("action=%q err=%v phase=%q", action, err, machine.Phase())
	}
	action, err = machine.RejectPlainText()
	if err != nil || action != ActionComplete || machine.Phase() != PhaseComplete {
		t.Fatalf("final action=%q err=%v phase=%q", action, err, machine.Phase())
	}
}
