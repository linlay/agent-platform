package tools

import (
	"context"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestRetiredMemoryToolsAreAbsentAndNotExecutable(t *testing.T) {
	executor, err := NewRuntimeToolExecutor(config.Config{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory_read", "memory_search", "memory_write", "memory_update"} {
		t.Run(name, func(t *testing.T) {
			for _, def := range executor.Definitions() {
				if def.Name == name {
					t.Fatalf("retired tool %s is still registered", name)
				}
			}
			// Even a stale session mounting the old name must not reach an implementation.
			result, err := executor.Invoke(context.Background(), name, nil, &contracts.ExecutionContext{Session: contracts.QuerySession{ToolNames: []string{name}}})
			if err != nil || result.Error != "tool_not_registered" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
