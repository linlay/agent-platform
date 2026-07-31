package llm

import (
	"context"
	"encoding/json"
	"testing"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
)

func TestRunStreamInterruptClosesWaitingApprovalBeforeCancel(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-interrupt-approval")
	waiting := &preparedToolInvocation{
		toolID:   "tool-waiting",
		toolName: "bash",
		args:     map[string]any{"command": "touch marker"},
	}
	queued := &preparedToolInvocation{
		toolID:   "tool-queued",
		toolName: "file_read",
		args:     map[string]any{"file_path": "README.md"},
	}
	stream := &llmRunStream{
		session:         contracts.QuerySession{RunID: "run-interrupt-approval", ChatID: "chat-interrupt-approval"},
		runControl:      control,
		hitlPendingCall: waiting,
		hitlAwaitingID:  "await-tool-waiting",
		hitlAwaitArgs:   map[string]any{"mode": "approval"},
		queuedToolCalls: []*preparedToolInvocation{queued},
	}
	if !control.Interrupt(contracts.InterruptInfo{
		Source: contracts.InterruptSourceHTTPAPI,
		Reason: contracts.InterruptReasonUserCancelled,
	}) {
		t.Fatal("interrupt was not accepted")
	}

	if err := stream.handleInterruptIfNeeded(); err != nil {
		t.Fatalf("handle interrupt: %v", err)
	}
	if len(stream.pending) != 4 {
		t.Fatalf("pending deltas = %#v, want awaiting answer, two tool results, cancel", stream.pending)
	}
	answer, ok := stream.pending[0].(contracts.DeltaAwaitingAnswer)
	if !ok {
		t.Fatalf("first delta = %#v, want awaiting answer", stream.pending[0])
	}
	if answer.AwaitingID != "await-tool-waiting" ||
		contracts.AnyStringNode(answer.Answer["mode"]) != "approval" ||
		contracts.AnyStringNode(answer.Answer["status"]) != "error" {
		t.Fatalf("unexpected awaiting answer %#v", answer)
	}
	answerError := contracts.AnyMapNode(answer.Answer["error"])
	if contracts.AnyStringNode(answerError["code"]) != string(apperrors.CodeRunInterrupted) ||
		contracts.AnyStringNode(answerError["reason"]) != string(apperrors.CodeRunInterrupted) {
		t.Fatalf("unexpected awaiting error %#v", answerError)
	}

	assertInterruptedToolResult(t, stream.pending[1], "tool-waiting", "await-tool-waiting", runInterruptedApprovalOutput)
	assertInterruptedToolResult(t, stream.pending[2], "tool-queued", "await-tool-waiting", runInterruptedExecutionOutput)
	if cancel, ok := stream.pending[3].(contracts.DeltaRunCancel); !ok || cancel.RunID != "run-interrupt-approval" {
		t.Fatalf("last delta = %#v, want run cancel", stream.pending[3])
	}
	if stream.hitlPendingCall != nil || len(stream.queuedToolCalls) != 0 {
		t.Fatalf("interrupted waiting state was not cleared: hitl=%#v queued=%#v", stream.hitlPendingCall, stream.queuedToolCalls)
	}
}

func TestRunStreamInterruptClosesEveryWaitingApprovalBatchCallOnce(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-interrupt-batch")
	first := &preparedToolInvocation{toolID: "tool-a", toolName: "bash", args: map[string]any{"command": "true"}}
	second := &preparedToolInvocation{toolID: "tool-b", toolName: "bash", args: map[string]any{"command": "false"}}
	stream := &llmRunStream{
		session:    contracts.QuerySession{RunID: "run-interrupt-batch"},
		runControl: control,
		hitlPendingBatch: &pendingHITLApprovalBatch{
			awaitingID:  "await-batch",
			invocations: []*preparedToolInvocation{first, second},
		},
		queuedToolCalls: []*preparedToolInvocation{first, second},
	}
	control.Interrupt(contracts.InterruptInfo{})

	if err := stream.handleInterruptIfNeeded(); err != nil {
		t.Fatalf("handle interrupt: %v", err)
	}
	if len(stream.pending) != 4 {
		t.Fatalf("pending deltas = %#v, want answer, two unique results, cancel", stream.pending)
	}
	assertInterruptedToolResult(t, stream.pending[1], "tool-a", "await-batch", runInterruptedApprovalOutput)
	assertInterruptedToolResult(t, stream.pending[2], "tool-b", "await-batch", runInterruptedApprovalOutput)
}

func TestRunStreamInterruptKeepsQueuedBatchCallOrder(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-interrupt-ordered-batch")
	ready := &preparedToolInvocation{toolID: "tool-ready-first", toolName: "file_read", args: map[string]any{"file_path": "README.md"}}
	waiting := &preparedToolInvocation{toolID: "tool-approval-second", toolName: "bash", args: map[string]any{"command": "touch marker"}}
	stream := &llmRunStream{
		session:    contracts.QuerySession{RunID: "run-interrupt-ordered-batch"},
		runControl: control,
		hitlPendingBatch: &pendingHITLApprovalBatch{
			awaitingID:  "await-ordered-batch",
			invocations: []*preparedToolInvocation{waiting},
		},
		queuedToolCalls: []*preparedToolInvocation{ready, waiting},
	}
	control.Interrupt(contracts.InterruptInfo{})

	if err := stream.handleInterruptIfNeeded(); err != nil {
		t.Fatalf("handle interrupt: %v", err)
	}
	assertInterruptedToolResult(t, stream.pending[1], "tool-ready-first", "await-ordered-batch", runInterruptedExecutionOutput)
	assertInterruptedToolResult(t, stream.pending[2], "tool-approval-second", "await-ordered-batch", runInterruptedApprovalOutput)
}

func TestRunStreamInterruptDoesNotFabricateResultForActiveExecution(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-interrupt-active")
	stream := &llmRunStream{
		session:    contracts.QuerySession{RunID: "run-interrupt-active"},
		runControl: control,
		activeToolCall: &preparedToolInvocation{
			toolID:   "tool-running",
			toolName: "bash",
			args:     map[string]any{"command": "touch marker"},
		},
	}
	control.Interrupt(contracts.InterruptInfo{})

	if err := stream.handleInterruptIfNeeded(); err != nil {
		t.Fatalf("handle interrupt: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("active execution must only emit run cancel, got %#v", stream.pending)
	}
	if _, ok := stream.pending[0].(contracts.DeltaRunCancel); !ok {
		t.Fatalf("delta = %#v, want run cancel", stream.pending[0])
	}
}

func TestRunStreamInterruptDoesNotFabricateResultForActiveConcurrentBatch(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-interrupt-active-batch")
	stream := &llmRunStream{
		session:    contracts.QuerySession{RunID: "run-interrupt-active-batch"},
		runControl: control,
		activeToolBatch: &activeToolBatch{
			invocations: []*preparedToolInvocation{{
				toolID:   "tool-running-in-batch",
				toolName: "file_write",
				args:     map[string]any{"file_path": "marker"},
			}},
			remaining: 1,
		},
	}
	control.Interrupt(contracts.InterruptInfo{})

	if err := stream.handleInterruptIfNeeded(); err != nil {
		t.Fatalf("handle interrupt: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("active batch must only emit run cancel, got %#v", stream.pending)
	}
	if _, ok := stream.pending[0].(contracts.DeltaRunCancel); !ok {
		t.Fatalf("delta = %#v, want run cancel", stream.pending[0])
	}
}

func assertInterruptedToolResult(t *testing.T, delta contracts.AgentDelta, toolID string, awaitingID string, output string) {
	t.Helper()
	result, ok := delta.(contracts.DeltaToolResult)
	if !ok {
		t.Fatalf("delta = %#v, want tool result", delta)
	}
	if result.ToolID != toolID || result.Result.Error != string(apperrors.CodeRunInterrupted) || result.Result.ExitCode != -1 {
		t.Fatalf("unexpected tool result %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Result.Output), &payload); err != nil {
		t.Fatalf("decode interrupted result %q: %v", result.Result.Output, err)
	}
	if contracts.AnyStringNode(payload["error"]) != string(apperrors.CodeRunInterrupted) ||
		contracts.AnyIntNode(payload["exitCode"]) != -1 ||
		contracts.AnyStringNode(payload["output"]) != output ||
		payload["executed"] != false ||
		contracts.AnyStringNode(payload["awaitingId"]) != awaitingID {
		t.Fatalf("unexpected interrupted payload %#v", payload)
	}
}
