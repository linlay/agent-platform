package platformcontrol

import (
	"agent-platform/internal/automation"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
	"context"
	"errors"
	"testing"
)

func TestAutomationBooleanTypeErrorBeforeReview(t *testing.T) {
	h := &ToolHandler{}
	params := map[string]any{"id": "task", "baseRevision": "revision", "enabled": "FALSE"}
	args := map[string]any{"action": "setEnabled", "args": params}
	e := controlExecution()
	approval, err := h.PrepareToolApproval(context.Background(), "automation_manage", args, e)
	var input *toolinput.Error
	if approval != nil || !errors.As(err, &input) || input.Field != "args.enabled" || input.Expected != "JSON boolean" || input.Actual != "string" {
		t.Fatalf("incorrect pre-review type error: %#v %v", approval, err)
	}
	result, err := h.Invoke(context.Background(), "automation_manage", args, e)
	if err != nil || result.Structured["executionState"] != "not_started" || result.Structured["field"] != "args.enabled" {
		t.Fatalf("invalid input reached execution: %+v %v", result, err)
	}
	if params["enabled"] != "FALSE" {
		t.Fatal("validation changed the requested value")
	}
	params["enabled"] = "false"
	_, admitted, err := h.admitted("automation_manage", args, e)
	if err != nil || admitted["enabled"] != false {
		t.Fatalf("corrected false was not preserved: %#v %v", admitted, err)
	}
}

func TestAutomationToolAdmissionApprovalAndPolicy(t *testing.T) {
	h := NewToolHandler(config.Config{}, nil, nil).ConfigureAutomation(&automation.Service{Registry: automation.NewRegistry(t.TempDir(), nil), ReceiptDir: t.TempDir(), DefaultZoneID: "UTC"})
	args := map[string]any{"action": "create", "args": map[string]any{"name": "Task", "agentKey": "agent", "cron": "0 9 * * *", "enabled": "false", "query": map[string]any{"message": "hello", "hidden": "false"}}}
	e := controlExecution()
	ctx := context.Background()
	p, err := h.PrepareToolApproval(ctx, "automation_manage", args, e)
	if err != nil {
		t.Fatal(err)
	}
	params := args["args"].(map[string]any)
	if params["enabled"] != false || params["query"].(map[string]any)["hidden"] != false {
		t.Fatal("review did not normalize boolean strings")
	}
	if !p.AllowAutoApprove || p.Form["action"] != "create" {
		t.Fatal("create policy")
	}
	result, _ := h.Invoke(ctx, "automation_manage", args, e)
	if result.Error == "" {
		t.Fatal("full access bypassed exact approval")
	}
	e.ToolApprovals = map[string]bool{p.Fingerprint: true}
	e.CurrentToolID = "sibling"
	result, _ = h.Invoke(ctx, "automation_manage", args, e)
	if result.Error == "" {
		t.Fatal("sibling reused approval")
	}
	e.CurrentToolID = "call"
	result, _ = h.Invoke(ctx, "automation_manage", args, e)
	if result.Error != "" {
		t.Fatalf("approved create: %+v", result)
	}
	item := result.Structured["automation"].(map[string]any)
	if item["enabled"] != false {
		t.Fatalf("approved false changed during execution: %#v", item["enabled"])
	}
	remove := map[string]any{"action": "delete", "args": map[string]any{"id": item["id"], "baseRevision": result.Structured["baseRevision"]}}
	e.CurrentToolID = "delete"
	p, err = h.PrepareToolApproval(ctx, "automation_manage", remove, e)
	if err != nil {
		t.Fatal(err)
	}
	if p.AllowAutoApprove {
		t.Fatal("delete must require human approval")
	}
	for _, alter := range []func(*contracts.ExecutionContext){
		func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil },
		func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" },
		func(e *contracts.ExecutionContext) { e.Session.TeamID = "team" },
		func(e *contracts.ExecutionContext) { e.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly },
	} {
		e := controlExecution()
		alter(e)
		if _, err := h.PrepareToolApproval(ctx, "automation_manage", args, e); err == nil {
			t.Fatal("invalid caller accepted")
		}
	}
	read := controlExecution()
	read.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly
	result, _ = h.Invoke(ctx, "automation_query", map[string]any{"action": "list"}, read)
	if result.Error != "" {
		t.Fatal("readonly query rejected")
	}
	result, _ = h.Invoke(ctx, "automation_query", map[string]any{"action": "list", "args": map[string]any{"limit": 1.5}}, read)
	if result.Error == "" {
		t.Fatal("fractional page size accepted")
	}
}
