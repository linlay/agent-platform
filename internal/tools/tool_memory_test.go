package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
)

func TestMarkdownMemoryWriteAuthorizationAndRevision(t *testing.T) {
	root := t.TempDir()
	store := memory.NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), nil)
	executor := &RuntimeToolExecutor{cfg: config.Config{Memory: config.MemoryConfig{Enabled: true}}, memory: store}
	session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "agent", Mode: "GENERAL", RunOwner: AgentRunOwner("agent", ""), AgentHasMemoryConfig: true}
	args := map[string]any{"kind": "memory", "operation": "save", "content": "verified fact", "revision": "missing"}
	for _, test := range []struct {
		name   string
		change func(*QuerySession)
	}{
		{"disabled", func(s *QuerySession) { s.AgentHasMemoryConfig = false }},
		{"child", func(s *QuerySession) { s.SubTaskID = "child" }},
		{"team", func(s *QuerySession) { s.TeamID = "team" }},
		{"derived run", func(s *QuerySession) { s.RunOrigin = &RunOrigin{} }},
		{"side run", func(s *QuerySession) { s.RunScopeID = "side" }},
		{"read only", func(s *QuerySession) { s.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := session
			test.change(&s)
			r, e := executor.invokeMemoryWrite("", args, &ExecutionContext{Session: s})
			if e != nil || r.Error == "" {
				t.Fatalf("write was not rejected: %+v %v", r, e)
			}
		})
	}
	invalid := map[string]any{"kind": "memory", "operation": "save", "revision": "missing"}
	r, e := executor.invokeMemoryWrite("", invalid, &ExecutionContext{Session: session})
	if e != nil || r.Error != "memory_invalid_document" {
		t.Fatalf("missing content: %+v %v", r, e)
	}
	r, e = executor.invokeMemoryWrite("", args, &ExecutionContext{Session: session})
	if e != nil || r.Error != "" {
		t.Fatalf("write: %+v %v", r, e)
	}
	r, e = executor.invokeMemoryWrite("", args, &ExecutionContext{Session: session})
	if e != nil || r.Error != "memory_conflict" {
		t.Fatalf("stale write: %+v %v", r, e)
	}
	d, e := store.Read("memory", "")
	if e != nil || !strings.Contains(d.Content, "verified fact") {
		t.Fatalf("content: %+v %v", d, e)
	}
}
