package llm

import (
	"testing"

	"agent-platform/internal/contracts"
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
