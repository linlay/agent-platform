package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"testing"
)

func TestToolResultRejectsBuiltinViewsWithoutChangingResult(t *testing.T) {
	for _, key := range []string{"approval", "question", "platform_control_review"} {
		t.Run(key, func(t *testing.T) {
			s := &llmRunStream{engine: &LLMAgentEngine{}, session: contracts.QuerySession{ModeToolDefinitions: []api.ToolDetailResponse{{Name: "lookup", Meta: map[string]any{"view": map[string]any{"key": key}}}}}}
			s.emitToolResult(&preparedToolInvocation{toolID: "call", toolName: "lookup"}, contracts.ToolExecutionResult{Output: "ok", Structured: map[string]any{"ok": true}}, false)
			delta := s.pending[0].(contracts.DeltaToolResult)
			if delta.View != nil || delta.ViewError != "invalid_view" || delta.Result.Structured["ok"] != true {
				t.Fatalf("result: %#v", delta)
			}
			ref, code := s.resolveView(map[string]any{"key": key}, "form")
			if ref == nil || code != "" {
				t.Fatalf("HITL view changed: %#v %s", ref, code)
			}
		})
	}
	s := &llmRunStream{engine: &LLMAgentEngine{}, session: contracts.QuerySession{ModeToolDefinitions: []api.ToolDetailResponse{{Name: "remote", Meta: map[string]any{"viewError": "invalid_view"}}}}}
	s.emitToolResult(&preparedToolInvocation{toolID: "call", toolName: "remote"}, contracts.ToolExecutionResult{Output: "ok", Structured: map[string]any{"ok": true}}, false)
	if delta := s.pending[0].(contracts.DeltaToolResult); delta.ViewError != "invalid_view" || delta.View != nil {
		t.Fatalf("remote diagnostic lost: %#v", delta)
	}
}
