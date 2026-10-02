package llm

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	runtimetools "agent-platform/internal/tools"
)

func TestWaitPreservesToolOrder(t *testing.T) {
	before := &preparedToolInvocation{toolID: "before", toolName: "datetime"}
	wait := &preparedToolInvocation{toolID: "wait", toolName: "wait"}
	after := &preparedToolInvocation{toolID: "after", toolName: "datetime"}
	s := &llmRunStream{execCtx: &ExecutionContext{}, queuedToolCalls: []*preparedToolInvocation{before, wait, after}}
	for _, want := range []*preparedToolInvocation{before, wait} {
		if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
			t.Fatal(err)
		}
		if s.activeToolCall != want || s.activeToolBatch != nil {
			t.Fatalf("sleep batch was reordered or parallelized: %#v", s)
		}
		s.activeToolCall = nil
	}
	if len(s.queuedToolCalls) != 1 || s.queuedToolCalls[0] != after {
		t.Fatal("lost following tool")
	}
}

func TestWaitSteerContinuesSameRun(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "batch"}[batch], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			control := NewRunControl(ctx, "run-sleep")
			defer control.Finish()
			executor, err := runtimetools.NewRuntimeToolExecutor(config.Config{}, nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			session := QuerySession{RunID: "run-sleep", ChatID: "chat-sleep", ToolNames: []string{"wait"}}
			s := &llmRunStream{ctx: ctx, engine: &LLMAgentEngine{tools: executor}, session: session, runControl: control,
				execCtx: &ExecutionContext{Session: session, RunControl: control, StartedAt: time.Now(), Budget: Budget{Tool: RetryPolicy{MaxCalls: 10}}}}
			call := &preparedToolInvocation{toolID: "sleep-1", toolName: "wait", args: map[string]any{"offset": "+1m"}}
			if batch {
				err = s.startToolCallBatch([]*preparedToolInvocation{call})
			} else {
				s.activeToolCall = call
				err = s.invokeActiveToolCallAndPostHook()
			}
			if err != nil {
				t.Fatal(err)
			}
			consume := func() {
				t.Helper()
				var err error
				if batch {
					err = s.consumeActiveToolBatch()
				} else {
					err = s.consumeActiveToolExecution()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			consume()
			if len(s.pending) != 1 {
				t.Fatalf("missing countdown: %#v", s.pending)
			}
			wait, ok := s.pending[0].(DeltaToolWait)
			if !ok || wait.ToolID != "sleep-1" || wait.DeadlineAt-wait.StartedAt != 60000 {
				t.Fatalf("bad wait: %#v", s.pending)
			}
			d := stream.NewDispatcher(stream.StreamRequest{RunID: session.RunID})
			mapper := NewDeltaMapper(session.RunID, session.ChatID, Budget{}, nil, nil)
			inputs := mapper.Map(wait)
			if len(inputs) != 1 {
				t.Fatalf("wait mapping: %#v", inputs)
			}
			events := d.Dispatch(inputs[0])
			if len(events) != 1 || events[0].Type != "tool.wait" || events[0].Data().String("runId") != "run-sleep" {
				t.Fatalf("bad public wait: %#v", events)
			}
			s.pending = nil
			if !control.EnqueueSteer(api.SteerRequest{RunID: "run-sleep", Message: "change direction"}) {
				t.Fatal("steer rejected")
			}
			consume()

			results := 0
			for _, delta := range s.pending {
				if result, ok := delta.(DeltaToolResult); ok {
					results++
					if result.ToolID != "sleep-1" || result.Result.Structured["reason"] != "steered" {
						t.Fatalf("bad wake result: %#v", result)
					}
				}
			}
			if results != 1 {
				t.Fatalf("expected unique result: %#v", s.pending)
			}

			s.appendPendingSteers()
			if s.session.RunID != "run-sleep" || control.Finished() || s.activeToolExecution != nil || s.activeToolBatch != nil {
				t.Fatal("sleep changed run lifecycle")
			}
			if len(s.messages) != 2 || s.messages[0].Role != "tool" || s.messages[1].Role != "user" || s.messages[1].Content != "change direction" {
				t.Fatalf("next model input: %#v", s.messages)
			}
			if len(control.DrainSteers()) != 0 {
				t.Fatal("steer duplicated")
			}
		})
	}
}

func TestChildCannotDrainOrCloseParentSteers(t *testing.T) {
	control := NewRunControl(context.Background(), "root")
	defer control.Finish()
	control.EnqueueSteer(api.SteerRequest{Message: "root input"})
	child := &llmRunStream{runControl: control, execCtx: &ExecutionContext{Session: QuerySession{SubTaskID: "child"}}}
	child.appendPendingSteers()
	if child.appendTailSteersBeforeFinish() {
		t.Fatal("child consumed root input")
	}
	child.closeSteers()
	if !control.EnqueueSteer(api.SteerRequest{Message: "another input"}) {
		t.Fatal("child closed parent steer gate")
	}
	if len(control.DrainSteers()) != 2 {
		t.Fatal("child drained parent input")
	}
}
