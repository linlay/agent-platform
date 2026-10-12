package contracts

import (
	"context"
	"fmt"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts/queryinput"
)

func TestReferenceCommitAndEnqueueAreAtomic(t *testing.T) {
	for _, closed := range []bool{false, true} {
		control := NewRunControl(context.Background(), "run")
		scope := NewRunReferences(nil)
		if closed {
			control.CloseSteers()
		}
		request := api.SteerRequest{RunID: "run", References: []api.Reference{{Type: "skill", ID: "docs"}}, PreparedMessages: []map[string]any{{"role": "user", "content": "read"}}}
		request.CommitReferences = func() error {
			return scope.Commit(ReferenceGrant{References: request.References, Roots: RunAccessRoots{ReadRoots: []string{"/selected"}, ReadonlyRoots: []string{"/selected"}}})
		}
		accepted := control.EnqueueSteer(request)
		if accepted == closed || (len(scope.References()) > 0) == closed || (len(control.DrainSteers()) > 0) == closed {
			t.Fatalf("closed=%v accepted=%v state=%v", closed, accepted, scope.References())
		}
		control.Finish()
	}
}

func TestReferenceLimitsAreCumulativeAndTypesRemainDistinct(t *testing.T) {
	scope := NewRunReferences(nil)
	for i := 0; i < MaxReferencedKbases; i++ {
		if err := scope.Commit(ReferenceGrant{References: []queryinput.Reference{{Type: "kbase", ID: fmt.Sprint(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.Commit(ReferenceGrant{References: []queryinput.Reference{{Type: "kbase", ID: "extra"}}, Roots: RunAccessRoots{ReadRoots: []string{"/must-not-commit"}}}); err == nil {
		t.Fatal("exceeded cumulative limit")
	}
	if len(scope.AccessRoots().ReadRoots) != 0 {
		t.Fatal("rejected grant changed roots")
	}
	if err := scope.Commit(ReferenceGrant{References: []queryinput.Reference{{Type: "skill", ID: "0"}, {Type: "chat", ID: "0"}}}); err != nil {
		t.Fatal(err)
	}
	if len(scope.References()) != MaxReferencedKbases+2 {
		t.Fatal("different reference types collided")
	}
}
