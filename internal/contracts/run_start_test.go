package contracts

import (
	"agent-platform/internal/contracts/queryinput"
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunStartRequestDigestIgnoresInternalState(t *testing.T) {
	base := RunStartRequest{AgentKey: "a", Message: "m", References: queryinput.MakeReferences("skill", []string{"s"})}
	request := RunStartRequestDigest(base)
	for name, changed := range map[string]RunStartRequest{
		"message":          {AgentKey: "a", Message: "other", References: queryinput.MakeReferences("skill", []string{"s"})},
		"target":           {AgentKey: "b", Message: "m", References: queryinput.MakeReferences("skill", []string{"s"})},
		"explicit default": {AgentKey: "a", Message: "m", References: queryinput.MakeReferences("skill", []string{"s"}), AccessLevel: AccessLevelAskForApproval},
		"chat":             {AgentKey: "a", Message: "m", References: queryinput.MakeReferences("skill", []string{"s"}), ChatID: "c"},
		"skills":           {AgentKey: "a", Message: "m"},
		"libraries":        {AgentKey: "a", Message: "m", References: append(queryinput.MakeReferences("skill", []string{"s"}), queryinput.MakeReferences("kbase", []string{"kb"})...)},
	} {
		if RunStartRequestDigest(changed) == request {
			t.Fatalf("%s did not change the request digest", name)
		}
	}
	// Trusted internal fields never affect what the model asked for.
	withInternal := base
	withInternal.Origin = RunOrigin{RunID: "r", ToolID: "t"}
	withInternal.Input = map[string]any{"message": "m"}
	if RunStartRequestDigest(withInternal) != request {
		t.Fatal("request digest depends on internal state")
	}
}

func TestAcceptAtAccessLevelExcludesUpdatesAndEndedRuns(t *testing.T) {
	control := NewRunControl(context.Background(), "run")
	control.UpdateAccessLevel(AccessLevelAutoApprove)
	_, before := control.AccessLevelSnapshot()
	updated := make(chan struct{})
	err := control.AcceptAtAccessLevel(func(level string, version int64) error {
		if level != AccessLevelAutoApprove || version != before {
			t.Fatalf("observed %s/%d", level, version)
		}
		go func() {
			control.UpdateAccessLevel(AccessLevelFullAccess)
			close(updated)
		}()
		select {
		case <-updated:
			t.Fatal("access level changed inside the acceptance point")
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-updated
	if level, after := control.AccessLevelSnapshot(); level != AccessLevelFullAccess || after == before {
		t.Fatalf("update lost: %s %d", level, after)
	}
	sentinel := errors.New("refused")
	if err := control.AcceptAtAccessLevel(func(string, int64) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	control.Interrupt(InterruptInfo{})
	called := false
	if err := control.AcceptAtAccessLevel(func(string, int64) error { called = true; return nil }); !errors.Is(err, ErrRunInterrupted) || called {
		t.Fatalf("interrupted run accepted: %v %v", err, called)
	}
}
