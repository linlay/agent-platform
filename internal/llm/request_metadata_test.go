package llm

import (
	"testing"

	"agent-platform/internal/contracts"
)

func TestCurrentInputMessagesForJSONLSkipsSystemAuditNotices(t *testing.T) {
	for _, tc := range []struct {
		name   string
		notice string
	}{
		{
			name: "hitl approval batch",
			notice: `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:`,
		},
		{
			name: "auto approval",
			notice: `[System audit — auto approval]
The system auto-approved the following tool call(s):`,
		},
		{
			name: "mixed approval batch",
			notice: `[System audit — approval batch]
The following tool call approval decisions were applied:`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &llmRunStream{
				session: contracts.QuerySession{
					CurrentMessages: []map[string]any{{"role": "user", "content": "hello"}},
				},
				messages: []openAIMessage{
					{Role: "system", Content: "system"},
					{Role: "assistant", Content: "calling tool"},
					{Role: "tool", ToolCallID: "tool-1", Name: "bash", Content: "ok"},
					{Role: "user", Content: tc.notice},
				},
			}

			if got := stream.currentInputMessagesForJSONL(); len(got) != 0 {
				t.Fatalf("expected audit notice to be skipped, got %#v", got)
			}
		})
	}
}

func TestCurrentInputMessagesForJSONLPreservesNonAuditUserInput(t *testing.T) {
	stream := &llmRunStream{
		session: contracts.QuerySession{
			CurrentMessages: []map[string]any{{"role": "user", "content": "hello"}},
		},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "internal task prompt"},
		},
	}

	got := stream.currentInputMessagesForJSONL()
	if len(got) != 1 || got[0]["content"] != "internal task prompt" {
		t.Fatalf("expected non-audit user input to remain, got %#v", got)
	}
}

func TestCurrentInputMessagesForJSONLFiltersAuditAndSkipsPendingSteer(t *testing.T) {
	stream := &llmRunStream{
		session: contracts.QuerySession{
			CurrentMessages: []map[string]any{{"role": "user", "content": "hello"}},
		},
		pendingSteerInputs: []map[string]any{{"role": "user", "content": "Please keep it short."}},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "tool", ToolCallID: "tool-1", Name: "bash", Content: "ok"},
			{Role: "user", Content: `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:`},
			{Role: "user", Content: "Please keep it short."},
		},
	}

	if got := stream.currentInputMessagesForJSONL(); len(got) != 0 {
		t.Fatalf("expected pending steer to be skipped from inputMessages, got %#v", got)
	}
}

func TestCurrentInputMessagesForJSONLPreservesNonSteerAfterDroppingSteer(t *testing.T) {
	stream := &llmRunStream{
		session: contracts.QuerySession{
			CurrentMessages: []map[string]any{{"role": "user", "content": "hello"}},
		},
		pendingSteerInputs: []map[string]any{{"role": "user", "content": "Please keep it short."}},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "Please keep it short."},
			{Role: "user", Content: "internal task prompt"},
		},
	}

	got := stream.currentInputMessagesForJSONL()
	if len(got) != 1 || got[0]["content"] != "internal task prompt" {
		t.Fatalf("expected non-steer input to remain, got %#v", got)
	}
}
