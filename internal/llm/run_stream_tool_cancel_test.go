package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type cancelResultExecutor struct{ started chan string }

func (*cancelResultExecutor) Definitions() []api.ToolDetailResponse { return nil }
func (e *cancelResultExecutor) Invoke(ctx context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	e.started <- execCtx.CurrentToolID
	<-ctx.Done()
	return contracts.ToolExecutionResult{Output: "actual partial output", Error: "command_canceled", ExitCode: -1}, nil
}

func TestToolCancellationPreservesActualResultsBeforeTerminal(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, interrupt := range []bool{false, true} {
			t.Run(fmtCase(parallel, interrupt), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				control := contracts.NewRunControl(ctx, "run-cancel")
				executor := &cancelResultExecutor{started: make(chan string, 2)}
				s := &llmRunStream{ctx: control.Context(), runControl: control,
					engine: &LLMAgentEngine{tools: executor}, session: contracts.QuerySession{RunID: "run-cancel"},
					execCtx: &contracts.ExecutionContext{StartedAt: time.Now()}}
				calls := []*preparedToolInvocation{{toolID: "a", toolName: "bash"}}
				if parallel {
					calls = append(calls, &preparedToolInvocation{toolID: "b", toolName: "bash"})
				}
				if parallel {
					if err := s.startToolCallBatch(calls); err != nil {
						t.Fatal(err)
					}
				} else {
					s.activeToolCall = calls[0]
					if err := s.startActiveToolExecution(calls[0]); err != nil {
						t.Fatal(err)
					}
				}
				for range calls {
					select {
					case <-executor.started:
					case <-time.After(time.Second):
						t.Fatal("tool not started")
					}
				}
				if interrupt {
					control.Interrupt(contracts.InterruptInfo{})
				} else {
					cancel()
				}
				seen := map[string]int{}
				for {
					delta, err := s.Next()
					if err != nil {
						if !errors.Is(err, context.Canceled) && !errors.Is(err, contracts.ErrRunInterrupted) {
							t.Fatal(err)
						}
						break
					}
					switch value := delta.(type) {
					case contracts.DeltaToolResult:
						seen[value.ToolID]++
						if value.Result.Output != "actual partial output" {
							t.Fatalf("lost actual result: %#v", value)
						}
					case contracts.DeltaRunCancel:
						if len(seen) != len(calls) {
							t.Fatal("terminal preceded tool results")
						}
					}
				}
				for _, call := range calls {
					if seen[call.toolID] != 1 {
						t.Fatalf("tool %s results=%d; want 1", call.toolID, seen[call.toolID])
					}
				}
			})
		}
	}
}

func fmtCase(parallel, interrupt bool) string {
	name := "serial"
	if parallel {
		name = "parallel"
	}
	if interrupt {
		return name + "/interrupt"
	}
	return name + "/context"
}

type cancellationExecutorFunc func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error)

func (cancellationExecutorFunc) Definitions() []api.ToolDetailResponse { return nil }
func (f cancellationExecutorFunc) Invoke(ctx context.Context, name string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return f(ctx, name, args, execCtx)
}

func TestToolCancellationKeepsCompletedResultWithFullOutputQueue(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		s := &llmRunStream{ctx: ctx, execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}}
		s.engine.tools = cancellationExecutorFunc(func(ctx context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
			for range 128 {
				if err := execCtx.ToolOutputSink.EmitToolOutput(ctx, contracts.ToolOutput{Stream: "stdout", Delta: "x"}); err != nil {
					return contracts.ToolExecutionResult{}, err
				}
			}
			return contracts.ToolExecutionResult{Output: "already succeeded"}, nil
		})
		s.activeToolCall = &preparedToolInvocation{toolID: "full", toolName: "bash"}
		if err := s.startActiveToolExecution(s.activeToolCall); err != nil {
			t.Fatal(err)
		}
		select {
		case <-s.activeToolExecution.completion.done:
		case <-time.After(time.Second):
			t.Fatal("completion blocked on output queue")
		}
		cancel()
		delta, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		result, ok := delta.(contracts.DeltaToolResult)
		if !ok || result.Result.Output != "already succeeded" || result.Result.Error != "" {
			t.Fatalf("completion lost to cancellation: %#v", delta)
		}
		if _, err := s.Next(); !errors.Is(err, context.Canceled) {
			t.Fatalf("want terminal cancellation, got %v", err)
		}
	}
}

func TestToolCancellationDeadlineAndLateReturns(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmtCase(parallel, false), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			defer close(release)
			startedTools := make(chan struct{}, 2)
			s := &llmRunStream{ctx: ctx, execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}, toolShutdownTimeout: 20 * time.Millisecond}
			s.engine.tools = cancellationExecutorFunc(func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
				startedTools <- struct{}{}
				<-release // Model an executor that does not honor cancellation.
				return contracts.ToolExecutionResult{Output: "late success"}, nil
			})
			calls := []*preparedToolInvocation{{toolID: "a", toolName: "bash"}, {toolID: "b", toolName: "bash"}}
			var completions []*toolCompletion
			if parallel {
				if err := s.startToolCallBatch(calls); err != nil {
					t.Fatal(err)
				}
				completions = s.activeToolBatch.completions
			} else {
				calls = calls[:1]
				s.activeToolCall = calls[0]
				if err := s.startActiveToolExecution(calls[0]); err != nil {
					t.Fatal(err)
				}
				completions = []*toolCompletion{s.activeToolExecution.completion}
			}
			for range calls {
				select {
				case <-startedTools:
				case <-time.After(time.Second):
					t.Fatal("tool not started")
				}
			}
			cancel()
			started := time.Now()
			for range calls {
				delta, err := s.Next()
				if err != nil {
					t.Fatal(err)
				}
				result, ok := delta.(contracts.DeltaToolResult)
				if !ok || result.Result.Error != "tool_execution_outcome_unknown" || result.Result.Structured["executed"] != nil {
					t.Fatalf("must not claim success or no effects: %#v", delta)
				}
			}
			if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
				t.Fatalf("cleanup exceeded shared deadline: %v", elapsed)
			}
			// Release the executor without leaving a test goroutine behind.
			for range calls {
				release <- struct{}{}
			}
			for _, completion := range completions {
				select {
				case <-completion.done:
				case <-time.After(time.Second):
					t.Fatal("late worker blocked")
				}
			}
			if _, err := s.Next(); !errors.Is(err, context.Canceled) {
				t.Fatalf("late result escaped terminal: %v", err)
			}
		})
	}
}

func TestToolCancellationDoesNotDuplicatePublishedBatchResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &llmRunStream{ctx: ctx, execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}}
	started := make(chan struct{}, 2)
	s.engine.tools = cancellationExecutorFunc(func(ctx context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
		started <- struct{}{}
		if execCtx.CurrentToolID == "b" {
			<-ctx.Done()
			return contracts.ToolExecutionResult{}, contracts.ErrRunInterrupted
		}
		return contracts.ToolExecutionResult{Output: "success a"}, nil
	})
	if err := s.startToolCallBatch([]*preparedToolInvocation{{toolID: "a", toolName: "bash"}, {toolID: "b", toolName: "bash"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("tool not started")
		}
	}
	first, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if result, ok := first.(contracts.DeltaToolResult); !ok || result.ToolID != "a" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	cancel()
	second, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if result, ok := second.(contracts.DeltaToolResult); !ok || result.ToolID != "b" || result.Result.Error != "tool_execution_outcome_unknown" {
		t.Fatalf("unexpected second result: %#v", second)
	}
	if _, err := s.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if len(s.messages) != 2 || s.messages[0].ToolCallID != "a" || s.messages[1].ToolCallID != "b" {
		t.Fatalf("model messages not ordered and unique: %#v", s.messages)
	}
}

func TestToolCancellationBeforeWorkerStartsDoesNotInvokeExecutor(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmtCase(parallel, false), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s := &llmRunStream{ctx: ctx, execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}}
			s.engine.tools = cancellationExecutorFunc(func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
				t.Error("executor entered after cancellation was already observed")
				return contracts.ToolExecutionResult{}, nil
			})
			call := &preparedToolInvocation{toolID: "not-started", toolName: "bash"}
			if parallel {
				if err := s.startToolCallBatch([]*preparedToolInvocation{call}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.activeToolCall = call
				if err := s.startActiveToolExecution(call); err != nil {
					t.Fatal(err)
				}
			}
			delta, err := s.Next()
			if err != nil {
				t.Fatal(err)
			}
			assertInterruptedToolResult(t, delta, call.toolID, "", runInterruptedExecutionOutput)
		})
	}
}

func TestToolCancellationLargeReadyBatchDoesNotBlockStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &llmRunStream{ctx: ctx, execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}}
	calls := make([]*preparedToolInvocation, 140)
	for index := range calls {
		calls[index] = &preparedToolInvocation{toolID: fmt.Sprintf("ready-%d", index), toolName: "bash", queuedResult: &contracts.ToolExecutionResult{Output: "ready"}}
	}
	if err := s.startToolCallBatch(calls); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range calls {
		delta, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if result, ok := delta.(contracts.DeltaToolResult); !ok || result.Result.Output != "ready" {
			t.Fatalf("lost ready result: %#v", delta)
		}
	}
	if _, err := s.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
}

func TestToolCancellationSynchronousExecutorRetainsUnknownOutcome(t *testing.T) {
	for _, invokeErr := range []error{contracts.ErrRunInterrupted, context.Canceled, context.DeadlineExceeded} {
		t.Run(invokeErr.Error(), func(t *testing.T) {
			s := &llmRunStream{ctx: context.Background(), execCtx: &contracts.ExecutionContext{}, engine: &LLMAgentEngine{}}
			s.engine.tools = cancellationExecutorFunc(func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
				return contracts.ToolExecutionResult{Output: "partial observation"}, invokeErr
			})
			if err := s.invokeToolAndPublishResult(&preparedToolInvocation{toolID: "sync", toolName: "ordinary"}); err != nil {
				t.Fatal(err)
			}
			result, ok := s.pending[0].(contracts.DeltaToolResult)
			if !ok || result.Result.Error != "tool_execution_outcome_unknown" || !strings.Contains(result.Result.Output, "partial observation") || result.Result.Structured["executed"] != nil {
				t.Fatalf("lost synchronous cancellation outcome: %#v", s.pending)
			}
		})
	}
}
