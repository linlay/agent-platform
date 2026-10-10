package runenvops

import (
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"context"
	"testing"
)

func root() *contracts.ExecutionContext {
	return &contracts.ExecutionContext{CurrentToolID: "tool-1", RunEnvironment: runenv.NewScope(runenv.Limits{}), Session: contracts.QuerySession{RunID: "run-1", AgentKey: "agent", Mode: "GENERAL", RunOwner: contracts.AgentRunOwner("agent"), ToolNames: []string{ToolName}}}
}
func call(t *testing.T, h *ToolHandler, c *contracts.ExecutionContext, op string, p map[string]any) contracts.ToolExecutionResult {
	t.Helper()
	r, err := h.Invoke(context.Background(), ToolName, map[string]any{"operation": op, "params": p}, c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestOperationsAndCallerBoundaries(t *testing.T) {
	h := NewToolHandler(config.RunEnvConfig{DenyKeys: []string{"PRIVATE"}})
	c := root()
	r := call(t, h, c, "set", map[string]any{"key": "DOCUMENT_ID", "value": "visible", "idempotencyKey": "visible-key", "expectedRevision": float64(0)})
	if r.Error != "" || r.Structured["revision"] != uint64(1) {
		t.Fatalf("set %#v", r)
	}
	c.CurrentToolID = "tool-2"
	if r := call(t, h, c, "set", map[string]any{"key": "OTHER", "value": "x"}); r.Error != "" {
		t.Fatal(r)
	}
	r = call(t, h, c, "set", map[string]any{"key": "DOCUMENT_ID", "value": "visible", "idempotencyKey": "visible-key", "expectedRevision": float64(0)})
	if r.Error != "" || r.Structured["revision"] != uint64(1) || r.Structured["idempotent"] != true {
		t.Fatalf("original revision %#v", r)
	}
	c.ToolExecutionPolicy = "read_only"
	if r := call(t, h, c, "list", map[string]any{}); r.Error != "" || r.Structured["variables"].(map[string]string)["DOCUMENT_ID"] != "visible" {
		t.Fatalf("list %#v", r)
	}
	if r := call(t, h, c, "explain", map[string]any{"key": "PATH"}); r.Error != "" || r.Structured["key"].(map[string]any)["allowed"] != false {
		t.Fatalf("explain %#v", r)
	}
	for _, op := range []string{"set", "unset", "update"} {
		if r := call(t, h, c, op, map[string]any{}); r.Error != "run_env_stage_forbidden" {
			t.Fatal(r)
		}
	}
	for _, mutate := range []func(*contracts.ExecutionContext){
		func(c *contracts.ExecutionContext) { c.Session.SubTaskID = "child" },
		func(c *contracts.ExecutionContext) { c.Session.ToolNames = nil },
		func(c *contracts.ExecutionContext) { c.Session.Mode = "ACP" },
		func(c *contracts.ExecutionContext) { c.Session.RunOwner = contracts.AgentRunOwner("other") },
		func(c *contracts.ExecutionContext) { c.RunEnvironment = nil },
	} {
		c := root()
		mutate(c)
		for _, op := range []string{"list", "explain", "set", "unset", "update"} {
			if r := call(t, h, c, op, map[string]any{}); r.Error != "run_env_unavailable" {
				t.Fatalf("%s %#v", op, r)
			}
		}
	}
}
func TestStrictUpdateParams(t *testing.T) {
	h := NewToolHandler(config.RunEnvConfig{})
	c := root()
	for _, p := range []map[string]any{
		{}, {"set": nil}, {"unset": nil}, {"set": map[string]any{"A": 12}}, {"unset": []any{12}},
		{"set": map[string]any{"A": "x"}, "expectedRevision": nil},
		{"set": map[string]any{"A": "x"}, "expectedRevision": -1},
		{"set": map[string]any{"A": "x"}, "expectedRevision": 1.5},
		{"set": map[string]any{"A": "x"}, "idempotencyKey": ""},
		{"set": map[string]any{"A": "x"}, "unknown": true},
	} {
		if r := call(t, h, c, "update", p); r.Error != "run_env_invalid_params" {
			t.Fatalf("%#v -> %#v", p, r)
		}
	}
	if r := call(t, h, c, "update", map[string]any{"set": map[string]any{"a": "x"}}); r.Error != "" {
		t.Fatal(r)
	}
	c.CurrentToolID = "unset"
	if r := call(t, h, c, "update", map[string]any{"unset": []any{"a"}}); r.Error != "" || r.Structured["revision"] != uint64(2) {
		t.Fatal(r)
	}
}

func TestTEAMRootCanUseOwnRunEnvironment(t *testing.T) {
	h := NewToolHandler(config.RunEnvConfig{})
	c := root()
	c.Session.Mode = "TEAM"
	if r := call(t, h, c, "list", map[string]any{}); r.Error != "" {
		t.Fatal(r)
	}
	c.Session.SubTaskID = "member"
	if r := call(t, h, c, "list", map[string]any{}); r.Error != "run_env_unavailable" {
		t.Fatal(r)
	}
}
