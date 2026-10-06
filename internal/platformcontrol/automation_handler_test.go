package platformcontrol

import (
	"agent-platform/internal/automation"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"context"
	"testing"
)

func TestAutomationToolAdmissionApprovalAndPolicy(t *testing.T) {
	h := NewToolHandler(config.Config{}, nil, nil).ConfigureAutomation(&automation.Service{Registry: automation.NewRegistry(t.TempDir(), nil), ReceiptDir: t.TempDir(), DefaultZoneID: "UTC"})
	args := map[string]any{"action": "create", "args": map[string]any{"name": "Task", "agentKey": "agent", "cron": "0 9 * * *", "query": map[string]any{"message": "hello"}}}
	e := controlExecution()
	ctx := context.Background()
	p, err := h.PrepareToolApproval(ctx, "automation_manage", args, e)
	if err != nil {
		t.Fatal(err)
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
