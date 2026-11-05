package runops

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"context"
	"testing"
)

func TestMailNewChatInheritsOnlySelfModelAndPreservesInput(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "r")
	exec := &contracts.ExecutionContext{RunControl: control, CurrentModel: &queryinput.QueryModelOptions{Key: "strong", ReasoningEffort: "HIGH"}}
	origin := contracts.RunOrigin{AgentKey: "lead", RunID: "r", ToolID: "tool"}
	h := &ToolHandler{}
	for _, target := range []string{"", "lead", "worker"} {
		args := map[string]any{"newChat": true, "message": "work"}
		if target != "" {
			args["agentKey"] = target
		}
		req, err := h.mailRequest(args, origin, exec)
		if err != nil {
			t.Fatal(err)
		}
		if !req.NewChat || req.Start.Input["newChat"] != true {
			t.Fatal(req)
		}
		if target == "worker" {
			if req.Start.ModelKey != "" || req.Start.ReasoningEffort != "" {
				t.Fatal(req.Start)
			}
		} else if req.Start.ModelKey != "strong" || req.Start.ReasoningEffort != "HIGH" {
			t.Fatal(req.Start)
		}
	}
	for _, args := range []map[string]any{{"message": "work", "newChat": true, "chatId": "existing"}, {"message": "work", "modelKey": "strong"}, {"message": "work", "role": "agent"}} {
		if _, err := h.mailRequest(args, origin, exec); err == nil {
			t.Fatal("invalid send accepted", args)
		}
	}
	req, err := h.mailRequest(map[string]any{"message": "more", "chatId": "existing"}, origin, exec)
	if err != nil || req.Start.ModelKey != "" || req.Start.ReasoningEffort != "" {
		t.Fatal(req, err)
	}
}

func TestRetainedRunControlsEnforceOwnershipAndChaining(t *testing.T) {
	service := newFakeRunToolService()
	origin := contracts.RunOrigin{AgentKey: "zenmi", Subject: "alice"}
	service.snapshots["opened"] = contracts.RunSnapshot{RunID: "opened", Status: "running", Origin: &origin}
	h := NewToolHandler(service, nil)
	exec := runToolExecContext("alice", "read")
	result, err := h.Invoke(context.Background(), StatusToolName, map[string]any{"runId": "opened"}, exec)
	if err != nil || result.Error != "" {
		t.Fatal(result, err)
	}
	other := runToolExecContext("bob", "read")
	result, _ = h.Invoke(context.Background(), StatusToolName, map[string]any{"runId": "opened"}, other)
	if result.Error != "run_not_owned" {
		t.Fatal(result)
	}
	exec.Session.RunOrigin = &origin
	result, _ = h.Invoke(context.Background(), InterruptToolName, map[string]any{"runId": "opened"}, exec)
	if result.Error != "run_chaining_not_allowed" {
		t.Fatal(result)
	}
	exec.Session.RunOrigin = nil
	result, err = h.Invoke(context.Background(), InterruptToolName, map[string]any{"runId": "opened"}, exec)
	if err != nil || result.Error != "" || len(service.interrupts) != 1 {
		t.Fatal(result, err)
	}
}
