package runexec

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestProcessorKeepsOnlyCommittedMainModelTurn(t *testing.T) {
	var assistant strings.Builder
	processor := NewProcessor(ProcessorOptions{AssistantText: &assistant})
	processor.BeginModelTurn("")
	processor.Decorate(&stream.EventData{Type: "content.delta", Payload: map[string]any{"delta": "discarded"}})
	processor.DiscardModelTurn("", true)
	processor.Decorate(&stream.EventData{Type: "content.snapshot", Payload: map[string]any{"text": "accepted"}})
	processor.CommitModelTurn("")
	processor.Decorate(&stream.EventData{Type: "content.delta", Payload: map[string]any{"taskId": "child", "delta": "hidden child"}})
	if got := assistant.String(); got != "accepted" {
		t.Fatalf("assistant text = %q, want accepted", got)
	}
}

func TestProcessorRecordsTerminalErrorAndClaimsFailure(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-1")
	processor := NewProcessor(ProcessorOptions{RunControl: control, RunUsage: &chat.UsageData{}})
	data, visible, err := processor.Consume(stream.NewEvent("run.error", map[string]any{
		"runId": "run-1", "error": map[string]any{"code": "tool_calls_exceeded", "message": "limit reached"},
	}))
	if err != nil || !visible || data.Type != "run.error" {
		t.Fatalf("consume = (%#v, %v, %v)", data, visible, err)
	}
	if got := processor.TerminalFinishReason(); got != "error" {
		t.Fatalf("terminal reason = %q", got)
	}
	if got := contracts.AnyStringNode(processor.TerminalErrorPayload()["code"]); got != "tool_calls_exceeded" {
		t.Fatalf("terminal error code = %q", got)
	}
}
