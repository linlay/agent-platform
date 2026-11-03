package automation

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestControlCallerCannotCreateOrMutateHigherPermissionTask(t *testing.T) {
	s := controlService(t)
	caller := contracts.NewRunControl(context.Background(), "caller")
	caller.SetInitialAccessLevel("auto_approve")
	args := createArgs()
	args["query"] = map[string]any{"message": "hello", "accessLevel": "full_access"}
	if _, err := s.PrepareControlForRun("create", args, "low-create", caller); err == nil {
		t.Fatal("disabled high-permission task accepted")
	}
	id, revision := controlResult(t, applyTestControl(t, s, "create", args, "admin-create"))
	for _, action := range []string{"update", "setEnabled", "trigger"} {
		patch := map[string]any{"id": id, "baseRevision": revision}
		if action == "update" {
			patch["name"] = "changed"
		}
		if action == "setEnabled" {
			patch["enabled"] = true
		}
		if _, err := s.PrepareControlForRun(action, patch, "low-"+action, caller); err == nil {
			t.Fatalf("%s allowed higher stored level", action)
		}
	}
	if _, err := s.PrepareControlForRun("setEnabled", map[string]any{"id": id, "baseRevision": revision, "enabled": false}, "stop", caller); err != nil {
		t.Fatal(err)
	}
	caller.UpdateAccessLevel("full_access")
	plan, err := s.PrepareControlForRun("create", args, "race", caller)
	if err != nil {
		t.Fatal(err)
	}
	caller.UpdateAccessLevel("default")
	consumed := false
	_, err = s.ExecuteControlForRun("create", args, "race", caller, func(digest string) bool { consumed = true; return digest == plan.Digest })
	if err == nil || !strings.Contains(err.Error(), "calling Run current level") || consumed {
		t.Fatalf("downgrade bypassed or consumed grant: %v %v", err, consumed)
	}
	caller.UpdateAccessLevel("full_access")
	if _, err := s.ExecuteControlForRun("create", args, "replayed", caller, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	caller.UpdateAccessLevel("default")
	if _, err := s.PrepareControlForRun("create", args, "replayed", caller); err != nil {
		t.Fatalf("completed receipt changed after downgrade: %v", err)
	}
	if _, err := s.ExecuteControlForRun("create", args, "replayed", caller, func(string) bool { return true }); err != nil {
		t.Fatalf("completed receipt did not replay: %v", err)
	}
}
