package llm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestCompletedToolResultKeepsRawMCPResult(t *testing.T) {
	raw := map[string]any{"content": []any{map[string]any{"type": "image", "data": "QUJDREVG"}}}
	partial := contracts.ToolExecutionResult{Output: "partial", MCPResult: raw}

	t.Run("execution error", func(t *testing.T) {
		result := completedToolResult(batchToolCallResult{result: partial, err: errors.New("schema violation")})
		if result.Error != "tool_execution_failed" || !reflect.DeepEqual(result.MCPResult, raw) {
			t.Fatalf("raw result lost: %#v", result)
		}
	})
	t.Run("cancellation keeps raw out of model output", func(t *testing.T) {
		result := completedToolResult(batchToolCallResult{result: partial, err: context.Canceled})
		if result.Error != "tool_execution_outcome_unknown" || !reflect.DeepEqual(result.MCPResult, raw) {
			t.Fatalf("raw result lost: %#v", result)
		}
		embedded, _ := result.Structured["partialResult"].(contracts.ToolExecutionResult)
		if embedded.Output != "partial" || embedded.MCPResult != nil {
			t.Fatalf("partialResult must omit the raw MCP result: %#v", embedded)
		}
		if strings.Contains(result.Output, "QUJDREVG") || strings.Contains(result.Output, "MCPResult") {
			t.Fatalf("raw MCP result leaked into model output: %s", result.Output)
		}
	})
}
