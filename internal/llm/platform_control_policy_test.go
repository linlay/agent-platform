package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestPlatformControlOperationAwareConcurrencyAndPlanningPolicy(t *testing.T) {
	stream := &llmRunStream{execCtx: &contracts.ExecutionContext{ToolExecutionPolicy: "read_only"}}
	read := &preparedToolInvocation{toolName: "platform_inspect", args: map[string]any{"action": "runtimeStatus", "args": map[string]any{}}}
	write := &preparedToolInvocation{toolName: "run_env", args: map[string]any{"operation": "set", "params": map[string]any{"key": "DOCUMENT_ID", "value": "value"}}}
	unknown := &preparedToolInvocation{toolName: "platform_inspect", args: map[string]any{"action": "futureAction"}}
	pin := &preparedToolInvocation{toolName: "chat_manage", args: map[string]any{"action": "setPinned", "args": map[string]any{"pinned": true}}}
	if stream.isConcurrentToolInvocation(pin) || !stream.readOnlyToolDenied("chat_manage", pin.args) {
		t.Fatal("Chat pin mutation must be a scheduling barrier and forbidden during planning")
	}

	if !stream.isConcurrentToolInvocation(read) {
		t.Fatal("read-only platform_control operation must remain concurrency eligible")
	}
	if stream.isConcurrentToolInvocation(write) {
		t.Fatal("run.env mutation must be a scheduling barrier")
	}
	if stream.readOnlyToolDenied("platform_inspect", read.args) {
		t.Fatal("planning stage rejected a read-only platform_control operation")
	}
	if !stream.readOnlyToolDenied("run_env", write.args) || !stream.readOnlyToolDenied("platform_inspect", unknown.args) {
		t.Fatal("planning stage accepted a mutation or unknown operation")
	}
	bash := &preparedToolInvocation{toolName: "bash", args: map[string]any{"command": "httpx run online-docx session"}}
	ordered := stream.prioritizeAwaitingToolCalls([]*preparedToolInvocation{bash, write})
	if len(ordered) != 2 || ordered[0] != bash || ordered[1] != write {
		t.Fatalf("barrier changed provider call order: %#v", ordered)
	}
}

func TestCatalogValidationHistoryAndStreamContentPolicy(t *testing.T) {
	for _, resourceType := range []string{"agent", "team", "skill", "connector"} {
		t.Run(resourceType, func(t *testing.T) {
			raw := `{"action":"validate","args":{"resourceType":"` + resourceType + `","resourceKey":"demo","content":"key: demo\nname: confidential-candidate\n"}}`
			calls := []openAIToolCall{{ID: "validate-1", Type: "function", Function: openAIFunctionCall{Name: "catalog_query", Arguments: raw}}}
			history := sanitizedToolCalls(calls)
			if calls[0].Function.Arguments != raw {
				t.Fatal("history redaction mutated execution arguments")
			}
			if sanitizedToolCalls(history)[0].Function.Arguments != history[0].Function.Arguments {
				t.Fatal("repeated history redaction changed arguments")
			}
			mapper := NewDeltaMapper("run-1", "chat-1", contracts.Budget{}, nil, nil)
			mapper.Map(contracts.DeltaToolCall{Index: 0, ID: "validate-1", Name: "catalog_query", ArgsDelta: raw})
			events := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"validate-1"}})
			if len(events) != 2 {
				t.Fatalf("unexpected events: %#v", events)
			}
			args := events[0].(stream.ToolArgs).Delta
			if args != history[0].Function.Arguments {
				t.Fatalf("SSE/history differ: %s / %s", args, history[0].Function.Arguments)
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(args), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed["args"].(map[string]any)) != 3 || !strings.Contains(args, "confidential-candidate") || strings.Contains(args, "contentBytes") {
				t.Fatalf("unsafe history: %s", args)
			}
		})
	}
}

func TestRunEnvPolicyAndArgumentsRemainObservable(t *testing.T) {
	s := &llmRunStream{execCtx: &contracts.ExecutionContext{ToolExecutionPolicy: "read_only"}}
	for _, op := range []string{"list", "explain", "set", "unset", "update", "unknown"} {
		args := map[string]any{"operation": op}
		inv := &preparedToolInvocation{toolName: "run_env", args: args}
		read := op == "list" || op == "explain"
		if s.readOnlyToolDenied("run_env", args) == read || s.isConcurrentToolInvocation(inv) != read || hasToolExecutionBarrier([]*preparedToolInvocation{inv}) == read {
			t.Fatalf("policy %s", op)
		}
	}
	for _, op := range []string{"set", "update", "unknown"} {
		raw := `{"operation":"` + op + `","params":{"value":"visible-value","set":{"A":"visible-value"},"idempotencyKey":"visible-retry-key"}}`
		calls := []openAIToolCall{{ID: "env-1", Type: "function", Function: openAIFunctionCall{Name: "run_env", Arguments: raw}}}
		if got := sanitizedToolCalls(calls)[0].Function.Arguments; got != raw {
			t.Fatalf("history changed %s", got)
		}
		mapper := NewDeltaMapper("run", "chat", contracts.Budget{}, nil, nil)
		events := mapper.Map(contracts.DeltaToolCall{Index: 0, ID: "env-1", Name: "run_env", ArgsDelta: raw})
		found := false
		for _, e := range events {
			if args, ok := e.(stream.ToolArgs); ok && args.Delta == raw {
				found = true
			}
		}
		if !found || len(mapper.sensitiveToolArgs) != 0 {
			t.Fatalf("buffered or redacted %#v", events)
		}
	}
}
