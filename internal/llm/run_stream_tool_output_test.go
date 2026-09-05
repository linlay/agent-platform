package llm

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type streamingOutputToolExecutor struct {
	release chan struct{}
}

func (e *streamingOutputToolExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (e *streamingOutputToolExecutor) SupportsToolOutput(toolName string, _ *contracts.ExecutionContext) bool {
	return toolName == "bash"
}

type parallelStreamingOutputToolExecutor struct{}

func (*parallelStreamingOutputToolExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (*parallelStreamingOutputToolExecutor) Invoke(ctx context.Context, _ string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	label, _ := args["label"].(string)
	if err := execCtx.ToolOutputSink.EmitToolOutput(ctx, contracts.ToolOutput{
		Stream: contracts.ToolOutputStdout,
		Delta:  label + "\n",
	}); err != nil {
		return contracts.ToolExecutionResult{}, err
	}
	return contracts.ToolExecutionResult{Output: "result-" + label, ExitCode: 0}, nil
}

func (e *streamingOutputToolExecutor) Invoke(ctx context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	if err := execCtx.ToolOutputSink.EmitToolOutput(ctx, contracts.ToolOutput{Stream: contracts.ToolOutputStdout, Delta: "qr\n"}); err != nil {
		return contracts.ToolExecutionResult{}, err
	}
	if err := execCtx.ToolOutputSink.EmitToolOutput(ctx, contracts.ToolOutput{Stream: contracts.ToolOutputStderr, Delta: "waiting\n"}); err != nil {
		return contracts.ToolExecutionResult{}, err
	}
	select {
	case <-e.release:
		return contracts.ToolExecutionResult{Output: "done", ExitCode: 0}, nil
	case <-ctx.Done():
		return contracts.ToolExecutionResult{}, ctx.Err()
	}
}

func TestActiveToolExecutionEmitsOutputBeforeUniqueResult(t *testing.T) {
	executor := &streamingOutputToolExecutor{release: make(chan struct{})}
	hookCalls := 0
	stream := &llmRunStream{
		ctx:     context.Background(),
		engine:  &LLMAgentEngine{tools: executor},
		session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1"},
		execCtx: &contracts.ExecutionContext{
			StartedAt: time.Now(),
			Budget:    contracts.Budget{Tool: contracts.RetryPolicy{MaxCalls: 10}},
		},
		activeToolCall: &preparedToolInvocation{toolID: "tool_1", toolName: "bash", args: map[string]any{"command": "login"}},
		postToolHook: func(_, _ string) contracts.PostToolHookResult {
			hookCalls++
			return contracts.PostToolContinue
		},
	}

	if err := stream.invokeActiveToolCallAndPostHook(); err != nil {
		t.Fatalf("start tool: %v", err)
	}
	if stream.activeToolExecution == nil || hookCalls != 0 {
		t.Fatalf("tool must remain active before result: execution=%#v hooks=%d", stream.activeToolExecution, hookCalls)
	}
	if capacity := cap(stream.activeToolExecution.eventCh); capacity != 128 {
		t.Fatalf("tool output channel capacity=%d want 128", capacity)
	}

	for index, want := range []struct {
		stream string
		delta  string
	}{{contracts.ToolOutputStdout, "qr\n"}, {contracts.ToolOutputStderr, "waiting\n"}} {
		if err := stream.consumeActiveToolExecution(); err != nil {
			t.Fatalf("consume output %d: %v", index, err)
		}
		if len(stream.pending) != 1 {
			t.Fatalf("output %d pending=%#v", index, stream.pending)
		}
		output, ok := stream.pending[0].(contracts.DeltaToolOutput)
		if !ok || output.ToolID != "tool_1" || output.ToolName != "bash" || output.Stream != want.stream || output.Delta != want.delta || output.ChunkIndex != index {
			t.Fatalf("unexpected output %d: %#v", index, stream.pending[0])
		}
		stream.pending = nil
	}
	if hookCalls != 0 {
		t.Fatalf("post hook ran before result: %d", hookCalls)
	}
	close(executor.release)
	if err := stream.consumeActiveToolExecution(); err != nil {
		t.Fatalf("consume result: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("result pending=%#v", stream.pending)
	}
	result, ok := stream.pending[0].(contracts.DeltaToolResult)
	if !ok || result.ToolID != "tool_1" || result.Result.Output != "done" {
		t.Fatalf("unexpected final result: %#v", stream.pending[0])
	}
	if stream.activeToolExecution != nil || stream.activeToolCall != nil || hookCalls != 1 {
		t.Fatalf("tool did not close exactly once: execution=%#v call=%#v hooks=%d", stream.activeToolExecution, stream.activeToolCall, hookCalls)
	}
}

func TestParallelToolOutputKeepsToolIdentityAndModelResultOrder(t *testing.T) {
	invocations := []*preparedToolInvocation{
		{toolID: "tool_a", toolName: "bash", args: map[string]any{"label": "a"}},
		{toolID: "tool_b", toolName: "bash", args: map[string]any{"label": "b"}},
	}
	stream := &llmRunStream{
		ctx:     context.Background(),
		engine:  &LLMAgentEngine{tools: &parallelStreamingOutputToolExecutor{}},
		session: contracts.QuerySession{RunID: "run_parallel", ChatID: "chat_parallel"},
		execCtx: &contracts.ExecutionContext{
			StartedAt: time.Now(),
			Budget:    contracts.Budget{Tool: contracts.RetryPolicy{MaxCalls: 10}},
		},
	}
	if err := stream.startToolCallBatch(invocations); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	seenOutput := map[string]string{}
	seenResult := map[string]bool{}
	for stream.activeToolBatch != nil {
		if err := stream.consumeActiveToolBatch(); err != nil {
			t.Fatalf("consume batch: %v", err)
		}
		for _, delta := range stream.pending {
			switch value := delta.(type) {
			case contracts.DeltaToolOutput:
				if seenResult[value.ToolID] {
					t.Fatalf("output arrived after result for %s", value.ToolID)
				}
				if value.ChunkIndex != 0 {
					t.Fatalf("first output index for %s=%d", value.ToolID, value.ChunkIndex)
				}
				seenOutput[value.ToolID] += value.Delta
			case contracts.DeltaToolResult:
				seenResult[value.ToolID] = true
			}
		}
		stream.pending = nil
	}
	if seenOutput["tool_a"] != "a\n" || seenOutput["tool_b"] != "b\n" || !seenResult["tool_a"] || !seenResult["tool_b"] {
		t.Fatalf("parallel outputs/results crossed identity: output=%#v result=%#v", seenOutput, seenResult)
	}
	if len(stream.messages) != 2 || stream.messages[0].ToolCallID != "tool_a" || stream.messages[1].ToolCallID != "tool_b" {
		t.Fatalf("model tool result messages lost original order: %#v", stream.messages)
	}
}

func TestHITLApprovedBashExecutionUsesLiveOutputRunner(t *testing.T) {
	executor := &streamingOutputToolExecutor{release: make(chan struct{})}
	invocation := &preparedToolInvocation{toolID: "tool_approved", toolName: "bash", args: map[string]any{"command": "login"}}
	stream := &llmRunStream{
		ctx:            context.Background(),
		engine:         &LLMAgentEngine{tools: executor},
		session:        contracts.QuerySession{RunID: "run_approved", ChatID: "chat_approved"},
		execCtx:        &contracts.ExecutionContext{StartedAt: time.Now()},
		activeToolCall: invocation,
	}
	if err := stream.executeOriginalBash(invocation); err != nil {
		t.Fatalf("execute approved bash: %v", err)
	}
	if stream.activeToolExecution == nil {
		t.Fatal("approved bash did not enter live output runner")
	}
	if err := stream.consumeActiveToolExecution(); err != nil {
		t.Fatalf("consume approved output: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("approved bash output pending=%#v", stream.pending)
	}
	if output, ok := stream.pending[0].(contracts.DeltaToolOutput); !ok || output.ToolID != "tool_approved" || output.Delta != "qr\n" {
		t.Fatalf("unexpected approved bash output: %#v", stream.pending[0])
	}
	stream.pending = nil
	if err := stream.consumeActiveToolExecution(); err != nil {
		t.Fatalf("consume second approved output: %v", err)
	}
	stream.pending = nil
	close(executor.release)
	if err := stream.consumeActiveToolExecution(); err != nil {
		t.Fatalf("consume approved result: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("approved bash result pending=%#v", stream.pending)
	}
	if result, ok := stream.pending[0].(contracts.DeltaToolResult); !ok || result.ToolID != "tool_approved" {
		t.Fatalf("unexpected approved bash result: %#v", stream.pending[0])
	}
}
