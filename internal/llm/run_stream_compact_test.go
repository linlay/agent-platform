package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	"agent-platform/internal/models"
)

type compactPendingInteractionExecutor struct {
	calls int
}

func (e *compactPendingInteractionExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (e *compactPendingInteractionExecutor) Invoke(_ context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	e.calls++
	execCtx.RunLoopState = contracts.RunLoopStateWaitingSubmit
	execCtx.RunControl.TransitionState(contracts.RunLoopStateWaitingSubmit)
	return contracts.ToolExecutionResult{}, contracts.ErrContextCompactPending
}

func TestBuildContextCompactPlanPinsCurrentInputAndKeepsToolPairAtomic(t *testing.T) {
	stream := &llmRunStream{
		model: models.ModelDefinition{ContextWindow: 160},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "old history"},
			{Role: "assistant", Content: "old answer"},
			{Role: "user", Content: "current root instruction"},
			{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "tool-1", Type: "function", Function: contracts.ModelFunctionCall{Name: "read", Arguments: "{}"}}}},
			{Role: "tool", ToolCallID: "tool-1", Content: "tool result"},
		},
		pinnedMessageStart: 3,
		pinnedMessageEnd:   4,
	}
	plan := stream.buildContextCompactPlan(true)
	if len(plan.pinned) != 1 || plan.pinned[0].Content != "current root instruction" {
		t.Fatalf("pinned messages = %#v", plan.pinned)
	}
	all := append(cloneModelMessages(plan.candidates), plan.retained...)
	foundCall, foundResult := false, false
	for _, message := range all {
		if len(message.ToolCalls) > 0 && message.ToolCalls[0].ID == "tool-1" {
			foundCall = true
		}
		if message.ToolCallID == "tool-1" {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("tool group was split candidates=%#v retained=%#v", plan.candidates, plan.retained)
	}
}

func TestManualCompactWaitsForStartedToolAndDoesNotRepeatIt(t *testing.T) {
	executor := &blockingToolExecutor{started: make(chan string, 1), release: make(chan struct{})}
	control := contracts.NewRunControl(context.Background(), "run-tool-compact")
	control.EnableContextCompact()
	stream := &llmRunStream{
		ctx:        context.Background(),
		engine:     &LLMAgentEngine{tools: executor},
		session:    contracts.QuerySession{RunID: "run-tool-compact", ChatID: "chat-tool-compact"},
		runControl: control,
		execCtx:    &contracts.ExecutionContext{StartedAt: time.Now()},
		model:      models.ModelDefinition{ContextWindow: 128000},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "assistant", Content: strings.Repeat("old context ", 1024)},
			{Role: "user", Content: "current instruction"},
		},
		pinnedMessageStart: 2,
		pinnedMessageEnd:   3,
		maxSteps:           3,
		allowToolUse:       true,
		currentTurn: &providerTurnStream{
			toolCalls:     map[int]*toolCallAccumulator{0: {ID: "tool-1", Type: "function", FunctionName: "datetime"}},
			finishReason:  "tool_calls",
			hasMeaningful: true,
		},
	}
	if err := stream.finishCurrentTurn(); err != nil {
		t.Fatalf("finishCurrentTurn: %v", err)
	}
	type nextResult struct {
		delta contracts.AgentDelta
		err   error
	}
	results := make(chan nextResult, 64)
	go func() {
		for {
			delta, err := stream.Next()
			results <- nextResult{delta: delta, err: err}
			if err != nil {
				return
			}
			if compact, ok := delta.(contracts.DeltaContextCompact); ok && compact.Status == "start" {
				return
			}
		}
	}()
	select {
	case name := <-executor.started:
		if name != "datetime" {
			t.Fatalf("started tool = %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	for len(results) > 0 {
		<-results
	}
	if _, status := control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "req-tool", CompactID: "compact-tool", ChatID: "chat-tool-compact", Trigger: "manual", Level: "summary"}); status != "queued" {
		t.Fatalf("enqueue status = %q", status)
	}
	select {
	case result := <-results:
		if result.err != nil && !errors.Is(result.err, io.EOF) {
			t.Fatalf("stream before release: %v", result.err)
		}
		t.Fatal("stream advanced while tool was still executing")
	case <-time.After(30 * time.Millisecond):
	}
	close(executor.release)
	foundToolResult := 0
	for {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("stream after release: %v", result.err)
			}
			switch delta := result.delta.(type) {
			case contracts.DeltaToolResult:
				if delta.ToolID == "tool-1" {
					foundToolResult++
				}
			case contracts.DeltaContextCompact:
				if delta.Status == "start" {
					if foundToolResult != 1 {
						t.Fatalf("tool result count before compact = %d", foundToolResult)
					}
					return
				}
			}
		case <-time.After(time.Second):
			t.Fatal("compact did not reach the post-tool safe point")
		}
	}
}

func TestManualCompactWakesHITLAndPreservesAwaitingState(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-hitl-compact")
	control.EnableContextCompact()
	awaitingID := buildHITLAwaitingID("tool-1")
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: awaitingID, Mode: "approval", ItemCount: 1})
	stream := &llmRunStream{
		ctx:        context.Background(),
		session:    contracts.QuerySession{RunID: "run-hitl-compact", ChatID: "chat-hitl-compact"},
		runControl: control,
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 60}},
		},
		model: models.ModelDefinition{ContextWindow: 128000},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "assistant", Content: strings.Repeat("old context ", 1024)},
			{Role: "user", Content: "current instruction"},
		},
		pinnedMessageStart: 2,
		pinnedMessageEnd:   3,
		hitlPendingCall:    &preparedToolInvocation{toolID: "tool-1", toolName: "bash", args: map[string]any{"command": "git push"}},
		hitlMatch:          &hitl.InterceptResult{Intercepted: true, Rule: hitl.FlatRule{Timeout: 60}},
		hitlAwaitingID:     awaitingID,
		hitlAwaitArgs:      map[string]any{"mode": "approval"},
	}
	done := make(chan error, 1)
	go func() { done <- stream.awaitHITLSubmitAndExecute() }()
	if _, status := control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "req-hitl", CompactID: "compact-hitl", ChatID: "chat-hitl-compact", Trigger: "manual", Level: "summary"}); status != "queued" {
		t.Fatalf("enqueue status = %q", status)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitHITLSubmitAndExecute: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("compact did not wake HITL wait")
	}
	if stream.hitlPendingCall == nil || stream.hitlAwaitingID != awaitingID || stream.compactWork == nil {
		t.Fatalf("HITL state was not preserved: call=%#v awaiting=%q work=%#v", stream.hitlPendingCall, stream.hitlAwaitingID, stream.compactWork)
	}
	if stream.compactWork.awaitingID != awaitingID || control.State() != contracts.RunLoopStateCompacting {
		t.Fatalf("compact awaiting/state = %q/%s", stream.compactWork.awaitingID, control.State())
	}
	start, ok := stream.pending[len(stream.pending)-1].(contracts.DeltaContextCompact)
	if !ok || start.Status != "start" || start.AwaitingID != awaitingID {
		t.Fatalf("compact start = %#v", stream.pending)
	}
	if _, ok := control.LookupAwaiting(awaitingID); !ok {
		t.Fatal("awaiting registration was cleared during compact")
	}
}

func TestManualCompactWakesInteractionToolAndKeepsSameCall(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-question-compact")
	control.EnableContextCompact()
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "tool-question", Mode: "question", ItemCount: 1, NoTimeout: true})
	if _, status := control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "req-question", CompactID: "compact-question", ChatID: "chat-question", Trigger: "manual", Level: "summary"}); status != "queued" {
		t.Fatalf("enqueue status = %q", status)
	}
	executor := &compactPendingInteractionExecutor{}
	stream := &llmRunStream{
		ctx:        context.Background(),
		engine:     &LLMAgentEngine{tools: executor},
		session:    contracts.QuerySession{RunID: "run-question-compact", ChatID: "chat-question"},
		runControl: control,
		execCtx: &contracts.ExecutionContext{
			RunControl: control,
		},
		model: models.ModelDefinition{ContextWindow: 128000},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "assistant", Content: strings.Repeat("old context ", 1024)},
			{Role: "user", Content: "current instruction"},
			{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "tool-question", Type: "function", Function: contracts.ModelFunctionCall{Name: "ask_user_question", Arguments: "{}"}}}},
		},
		pinnedMessageStart: 2,
		pinnedMessageEnd:   3,
		activeToolCall:     &preparedToolInvocation{toolID: "tool-question", toolName: "ask_user_question", args: map[string]any{}},
	}

	if err := stream.invokeActiveToolCallAndPostHook(); err != nil {
		t.Fatalf("invoke interaction: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("interaction calls = %d, want 1", executor.calls)
	}
	if stream.activeToolCall == nil || stream.activeToolCall.toolID != "tool-question" {
		t.Fatalf("active interaction call was not preserved: %#v", stream.activeToolCall)
	}
	if stream.compactWork == nil || stream.compactWork.awaitingID != "tool-question" {
		t.Fatalf("compact work = %#v, want awaiting tool-question", stream.compactWork)
	}
	if control.State() != contracts.RunLoopStateCompacting {
		t.Fatalf("run state = %s, want compacting", control.State())
	}
	if _, ok := control.LookupAwaiting("tool-question"); !ok {
		t.Fatal("awaiting registration was cleared during compact")
	}
	start, ok := stream.pending[len(stream.pending)-1].(contracts.DeltaContextCompact)
	if !ok || start.Status != "start" || start.AwaitingID != "tool-question" {
		t.Fatalf("compact start = %#v", stream.pending)
	}
}

func TestScheduleManualContextCompactTransitionsRunToCompacting(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-1")
	control.EnableContextCompact()
	if _, status := control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "req-1", CompactID: "compact-1", ChatID: "chat-1"}); status != "queued" {
		t.Fatalf("enqueue status = %q", status)
	}
	stream := &llmRunStream{
		session:    contracts.QuerySession{RunID: "run-1", ChatID: "chat-1"},
		runControl: control,
		execCtx:    &contracts.ExecutionContext{RunLoopState: contracts.RunLoopStateModelStreaming},
		model:      models.ModelDefinition{ContextWindow: 128000},
		messages: []openAIMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "old history"},
			{Role: "user", Content: "current"},
		},
		pinnedMessageStart: 2,
		pinnedMessageEnd:   3,
	}
	if !stream.scheduleContextCompact(false) {
		t.Fatal("manual compact was not scheduled")
	}
	if control.State() != contracts.RunLoopStateCompacting || stream.execCtx.RunLoopState != contracts.RunLoopStateCompacting {
		t.Fatalf("states control=%s exec=%s", control.State(), stream.execCtx.RunLoopState)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("pending deltas = %d", len(stream.pending))
	}
	if delta, ok := stream.pending[0].(contracts.DeltaContextCompact); !ok || delta.Status != "start" || delta.Trigger != "manual" {
		t.Fatalf("compact start delta = %#v", stream.pending[0])
	}
}

func TestProviderContextOverflowCompactsOnceThenReturnsUncompactable(t *testing.T) {
	contextErr := apperrors.New(
		apperrors.CodeProviderContextLengthExceeded,
		"too many tokens",
		apperrors.WithCategory(apperrors.CategoryModel),
		apperrors.WithScope(apperrors.ScopeModel),
	)
	stream := &llmRunStream{
		modelCall:   &pendingModelCall{attempt: 1, maxAttempts: 1},
		currentTurn: &providerTurnStream{},
	}
	if err := stream.handleModelAttemptError(contextErr); err != nil {
		t.Fatalf("first overflow: %v", err)
	}
	if !stream.forceContextCompact || !stream.contextOverflowRecoveryUsed || stream.modelTerminalError != nil {
		t.Fatalf("first overflow state force=%t used=%t terminal=%v", stream.forceContextCompact, stream.contextOverflowRecoveryUsed, stream.modelTerminalError)
	}

	stream.modelCall = &pendingModelCall{attempt: 1, maxAttempts: 1}
	stream.currentTurn = &providerTurnStream{}
	if err := stream.handleModelAttemptError(contextErr); err != nil {
		t.Fatalf("second overflow: %v", err)
	}
	var terminal *apperrors.Error
	if !errors.As(stream.modelTerminalError, &terminal) || terminal.Code() != apperrors.CodeContextWindowUncompactable {
		t.Fatalf("terminal error = %v, want context_window_uncompactable", stream.modelTerminalError)
	}
}
