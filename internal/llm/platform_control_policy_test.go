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
	read := &preparedToolInvocation{toolName: "platform_control", args: map[string]any{"operation": "runtime.status", "params": map[string]any{}}}
	write := &preparedToolInvocation{toolName: "platform_control", args: map[string]any{"operation": "run.env.set", "params": map[string]any{"key": "DOCUMENT_ID", "value": "value"}}}
	unknown := &preparedToolInvocation{toolName: "platform_control", args: map[string]any{"operation": "future.operation"}}

	if !stream.isConcurrentToolInvocation(read) {
		t.Fatal("read-only platform_control operation must remain concurrency eligible")
	}
	if stream.isConcurrentToolInvocation(write) {
		t.Fatal("run.env mutation must be a scheduling barrier")
	}
	if stream.readOnlyToolDenied("platform_control", read.args) {
		t.Fatal("planning stage rejected a read-only platform_control operation")
	}
	if !stream.readOnlyToolDenied("platform_control", write.args) || !stream.readOnlyToolDenied("platform_control", unknown.args) {
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
			raw := `{"operation":"catalog.validate","params":{"resourceType":"` + resourceType + `","resourceKey":"demo","content":"key: demo\nname: confidential-candidate\n"}}`
			calls := []openAIToolCall{{ID: "validate-1", Type: "function", Function: openAIFunctionCall{Name: "platform_control", Arguments: raw}}}
			history := sanitizedToolCalls(calls)
			if calls[0].Function.Arguments != raw {
				t.Fatal("history redaction mutated execution arguments")
			}
			if sanitizedToolCalls(history)[0].Function.Arguments != history[0].Function.Arguments {
				t.Fatal("repeated history redaction changed arguments")
			}
			mapper := NewDeltaMapper("run-1", "chat-1", contracts.Budget{}, nil, nil)
			mapper.Map(contracts.DeltaToolCall{Index: 0, ID: "validate-1", Name: "platform_control", ArgsDelta: raw})
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
			if len(parsed["params"].(map[string]any)) != 3 || !strings.Contains(args, "confidential-candidate") || strings.Contains(args, "contentBytes") {
				t.Fatalf("unsafe history: %s", args)
			}
		})
	}
}
