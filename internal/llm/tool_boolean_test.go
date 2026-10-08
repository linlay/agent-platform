package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"testing"
)

func TestPrepareToolCallBooleanNormalizationPreservesRawAndMCP(t *testing.T) {
	for _, source := range []string{"builtin", "mcp"} {
		executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{{Name: "boolean_probe", Parameters: map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}, "text": map[string]any{"type": "string"}}}, Meta: map[string]any{"sourceType": source}}}}
		session := contracts.QuerySession{ToolNames: []string{"boolean_probe"}}
		s := &llmRunStream{session: session, engine: &LLMAgentEngine{tools: executor}, execCtx: &contracts.ExecutionContext{Session: session}}
		raw := `{"enabled":"false","text":"true"}`
		call := openAIToolCall{ID: "probe", Type: "function", Function: openAIFunctionCall{Name: "boolean_probe", Arguments: raw}}
		inv, _, msg := s.prepareToolCall(call)
		if inv == nil || msg != nil {
			t.Fatalf("%s: %#v", source, msg)
		}
		var want any = false
		if source == "mcp" {
			want = "false"
		}
		if inv.args["enabled"] != want || inv.args["text"] != "true" || call.Function.Arguments != raw {
			t.Fatalf("%s: %#v", source, inv.args)
		}
	}
}
