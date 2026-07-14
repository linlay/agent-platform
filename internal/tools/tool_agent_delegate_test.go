package tools

import (
	"context"
	"testing"
)

func TestRuntimeToolExecutorRejectsDirectAgentDelegateInvocation(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).Invoke(context.Background(), "agent_delegate", map[string]any{
		"tasks": []any{map[string]any{"agentKey": "writer"}},
	}, nil)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Error != "internal_tool_only" || result.ExitCode != -1 {
		t.Fatalf("unexpected direct agent_delegate result: %#v", result)
	}
}
