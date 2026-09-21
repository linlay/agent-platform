package llm

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/credentialview"
	"agent-platform/internal/stream"
	"strings"
	"testing"
)

func TestCredentialMutationHistoryAndChunkedStream(t *testing.T) {
	policy := credentialview.Policy{Providers: "/runtime/registries/providers"}
	raw := `{"file_path":"/runtime/registries/providers/demo.yml","content":"key: demo\napiKey: private-value\n"}`
	calls := []openAIToolCall{{ID: "write-1", Type: "function", Function: openAIFunctionCall{Name: "file_write", Arguments: raw}}}
	history := sanitizedToolCalls(calls, policy)
	if strings.Contains(history[0].Function.Arguments, "private-value") || calls[0].Function.Arguments != raw {
		t.Fatal("unsafe or mutated history")
	}
	mapper := NewDeltaMapper("run-1", "chat-1", contracts.Budget{}, nil, nil)
	mapper.credentialPolicy = policy
	for i, chunk := range []string{raw[:70], raw[70:]} {
		id, name := "", ""
		if i == 0 {
			id, name = "write-1", "file_write"
		}
		if events := mapper.Map(contracts.DeltaToolCall{Index: 0, ID: id, Name: name, ArgsDelta: chunk}); len(events) != 0 {
			t.Fatal("partial arguments leaked", events)
		}
	}
	events := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"write-1"}})
	if len(events) != 2 {
		t.Fatal(events)
	}
	args := events[0].(stream.ToolArgs).Delta
	if args != history[0].Function.Arguments {
		t.Fatal("SSE/history differ", args)
	}
	cloned := mapper.CloneIsolated("run-2", "chat-2").(*DeltaMapper)
	if cloned.credentialPolicy != policy {
		t.Fatal("clone lost policy")
	}
	trace := &llmChatTrace{enabled: true, path: t.TempDir() + "/trace.json", payload: map[string]any{}, credentialPolicy: policy}
	trace.appendToolCalls(calls)
	got := trace.payload["toolCalls"].([]any)[0].(map[string]any)["rawArguments"].(string)
	if got != args {
		t.Fatal("trace/history differ", got)
	}
}
func TestCredentialMutationRelativePathContext(t *testing.T) {
	root := t.TempDir()
	policy := credentialview.Policy{Providers: root + "/registries/providers"}
	session := contracts.QuerySession{WorkspaceRoot: root}
	raw := `{"file_path":"registries/providers/demo.yml","content":"key: demo\napiKey: private-value\n"}`
	mapper := NewDeltaMapper("r", "c", contracts.Budget{}, nil, nil)
	mapper.credentialPolicy = policy
	mapper.Map(contracts.DeltaToolCall{Index: 0, ID: "w", Name: "file_write", ArgsDelta: raw, PathSession: &session})
	events := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"w"}})
	got := events[0].(stream.ToolArgs).Delta
	if strings.Contains(got, "private-value") || !strings.Contains(got, "key: demo") {
		t.Fatal(got)
	}
	if len(mapper.toolPathSessions) != 0 {
		t.Fatal("path context retained after call")
	}
	s := &llmRunStream{session: session, engine: &LLMAgentEngine{}}
	s.engine.cfg.Providers.ExternalDir = policy.Providers
	calls := []openAIToolCall{{Function: openAIFunctionCall{Name: "file_write", Arguments: raw}}}
	history := s.sanitizedToolCalls(calls)
	if history[0].Function.Arguments != got {
		t.Fatal("relative path SSE/history mismatch")
	}
	// The normal stream, including isolated sub-runs, supplies its own context.
	s.pending = []contracts.AgentDelta{contracts.DeltaToolCall{Index: 0, ID: "w", Name: "file_write", ArgsDelta: raw}}
	delta, err := s.Next()
	if err != nil || delta.(contracts.DeltaToolCall).PathSession != &s.session {
		t.Fatal("missing execution path context", err)
	}
}
