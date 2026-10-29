package contracts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/scriptstate"
	"agent-platform/internal/skillsexec"
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

func TestInheritedSkillsRemainOutsideReferencesAndDoNotReviveRevokedEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "scripts", "task.sh")
	if err := os.WriteFile(file, []byte("echo original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := scriptstate.Owner{Agent: "agent", Run: "run", Environment: "host:"}
	scripts := skillsexec.New(owner, nil)
	scope := NewRunReferences(scripts)
	grant := func() ReferenceGrant {
		return ReferenceGrant{References: queryinput.MakeReferences("skill", []string{"inherited"}),
			Roots:   RunAccessRoots{ReadRoots: []string{root}, ReadonlyRoots: []string{root}},
			Scripts: map[string]*skillsexec.Scope{"inherited": skillsexec.New(owner, []skillsexec.Root{{Host: root}})}}
	}
	scope.Inherit(grant())
	if len(scope.References()) != 0 || len(scope.AccessRoots().ReadRoots) != 1 || len(scope.AccessRoots().ReadonlyRoots) != 0 || !scripts.Matches(owner, file, "", false) {
		t.Fatalf("inheritance changed references/readonly or missed script: %+v", scope.AccessRoots())
	}
	// An inherited skill does not consume any of the explicit reference slots.
	for i := 0; i < MaxReferencedSkills; i++ {
		if err := scope.Commit(ReferenceGrant{References: queryinput.MakeReferences("skill", []string{fmt.Sprint(i)})}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.Commit(ReferenceGrant{References: queryinput.MakeReferences("skill", []string{"extra"})}); err == nil {
		t.Fatal("explicit reference limit lost")
	}
	if err := os.WriteFile(file, []byte("echo changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if scripts.Matches(owner, file, "", false) {
		t.Fatal("changed inherited script kept its grant")
	}
	// Exercise the same explicit Commit/Merge path a later steer uses.
	other := NewRunReferences(scripts)
	if err := other.Commit(grant()); err != nil {
		t.Fatal(err)
	}
	if scripts.Matches(owner, file, "", false) {
		t.Fatal("explicit re-selection revived revoked credential")
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
