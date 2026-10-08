package contracts

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveRunStartAccessLevel(t *testing.T) {
	levels := []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess}
	for parentRank, parent := range levels {
		if level, escalates := ResolveRunStartAccessLevel("", parent); level != parent || escalates {
			t.Fatalf("omitted must inherit %s: %s %v", parent, level, escalates)
		}
		for requestedRank, requested := range levels {
			level, escalates := ResolveRunStartAccessLevel(requested, parent)
			if level != requested || escalates != (requestedRank > parentRank) {
				t.Fatalf("parent=%s requested=%s: %s %v", parent, requested, level, escalates)
			}
		}
	}
}

func TestRunStartDigestsSeparateIdempotencyFromApproval(t *testing.T) {
	base := RunStartRequest{AgentKey: "a", Message: "m", MustUseSkills: []string{"s"}}
	request := RunStartRequestDigest(base)
	for name, changed := range map[string]RunStartRequest{
		"message":          {AgentKey: "a", Message: "other", MustUseSkills: []string{"s"}},
		"target":           {TeamID: "a", Message: "m", MustUseSkills: []string{"s"}},
		"explicit default": {AgentKey: "a", Message: "m", MustUseSkills: []string{"s"}, AccessLevel: AccessLevelDefault},
		"chat":             {AgentKey: "a", Message: "m", MustUseSkills: []string{"s"}, ChatID: "c"},
		"skills":           {AgentKey: "a", Message: "m"},
	} {
		if RunStartRequestDigest(changed) == request {
			t.Fatalf("%s did not change the request digest", name)
		}
	}
	// Trusted internal fields never affect what the model asked for.
	withInternal := base
	withInternal.Origin = RunOrigin{RunID: "r", ToolID: "t"}
	withInternal.Review = &RunStartReview{ParentAccessVersion: 9}
	if RunStartRequestDigest(withInternal) != request {
		t.Fatal("request digest depends on internal state")
	}
	approval := RunStartApprovalDigest(request, AccessLevelAutoApprove, 3, AccessLevelFullAccess)
	for _, other := range []string{
		RunStartApprovalDigest(request, AccessLevelAutoApprove, 4, AccessLevelFullAccess),
		RunStartApprovalDigest(request, AccessLevelDefault, 3, AccessLevelFullAccess),
		RunStartApprovalDigest(request, AccessLevelAutoApprove, 3, AccessLevelAutoApprove),
		RunStartApprovalDigest("other", AccessLevelAutoApprove, 3, AccessLevelFullAccess),
	} {
		if other == approval {
			t.Fatal("approval digest ignores part of the reviewed baseline")
		}
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
