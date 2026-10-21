package llm

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

type modelUsageToolExecutor struct{}

func (modelUsageToolExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (modelUsageToolExecutor) Invoke(_ context.Context, _ string, args map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	result := contracts.ToolExecutionResult{Output: `{"ok":true}`, ExitCode: 0}
	if args["usage"] == true {
		result.ModelUsage = []contracts.ModelUsage{{
			ModelKey: "image-model", Operation: "image.generation",
			PromptTokens: 12, CompletionTokens: 1000, TotalTokens: 1012, CacheHitTokens: 2, CacheMissTokens: 10,
		}}
	}
	if args["characters"] == true {
		result.ModelUsage = []contracts.ModelUsage{{
			ModelKey: "speech-model", Operation: "speech.generation", Characters: 42, UpstreamRequestID: "trace-1",
		}}
	}
	return result, nil
}

func TestToolModelUsageCountsTowardRunWithoutTouchingContext(t *testing.T) {
	stream := &llmRunStream{
		ctx:     context.Background(),
		engine:  &LLMAgentEngine{tools: modelUsageToolExecutor{}},
		session: contracts.QuerySession{RunID: "run_tool_usage", ChatID: "chat_tool_usage"},
		model:   models.ModelDefinition{Key: "chat-model", ContextWindow: 128000},
		execCtx: &contracts.ExecutionContext{
			StartedAt: time.Now(),
			Budget:    contracts.Budget{Tool: contracts.RetryPolicy{MaxCalls: 10}},
		},
	}
	stream.commitUsage(&openAIUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	stream.pending = nil

	invocations := []*preparedToolInvocation{
		{toolID: "tool_image", toolName: "image_generate", args: map[string]any{"usage": true}},
		{toolID: "tool_plain", toolName: "image_generate", args: map[string]any{"usage": false}},
	}
	if err := stream.startToolCallBatch(invocations); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	var snapshots []contracts.DeltaUsageSnapshot
	for stream.activeToolBatch != nil {
		if err := stream.consumeActiveToolBatch(); err != nil {
			t.Fatalf("consume batch: %v", err)
		}
		for _, delta := range stream.pending {
			if snapshot, ok := delta.(contracts.DeltaUsageSnapshot); ok {
				snapshots = append(snapshots, snapshot)
			}
		}
		stream.pending = nil
	}

	if len(snapshots) != 1 {
		t.Fatalf("expected one tool usage snapshot (missing usage counts as zero), got %#v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.Tool == nil || snapshot.Tool.ToolID != "tool_image" || snapshot.Tool.ModelKey != "image-model" || snapshot.Tool.Operation != "image.generation" {
		t.Fatalf("unexpected tool marker %#v", snapshot.Tool)
	}
	if snapshot.ModelKey != "chat-model" || snapshot.ContextWindow != 128000 || snapshot.CurrentContextSize != 100 {
		t.Fatalf("context window must keep describing the agent model: %#v", snapshot)
	}
	if snapshot.LLMReturnPromptTokens != 12 || snapshot.LLMReturnCompletionTokens != 1000 || snapshot.LLMReturnTotalTokens != 1012 ||
		snapshot.LLMReturnPromptCacheHitTokens != 2 || snapshot.LLMReturnPromptCacheMissTokens != 10 ||
		snapshot.LLMReturnLLMChatCompletionCount != 0 || snapshot.LLMReturnToolCallCount != 0 {
		t.Fatalf("unexpected current tool usage %#v", snapshot)
	}
	if snapshot.RunPromptTokens != 112 || snapshot.RunCompletionTokens != 1020 || snapshot.RunTotalTokens != 1132 {
		t.Fatalf("run usage must include the tool call: %#v", snapshot)
	}
	if stream.lastCallPromptTokens != 100 || stream.lastCallCompletionTokens != 20 {
		t.Fatalf("last call must stay the agent model call: prompt=%d completion=%d", stream.lastCallPromptTokens, stream.lastCallCompletionTokens)
	}

	stream.commitUsage(&openAIUsage{PromptTokens: 200, CompletionTokens: 10, TotalTokens: 210})
	next, ok := stream.pending[len(stream.pending)-1].(contracts.DeltaUsageSnapshot)
	if !ok || next.Tool != nil || next.RunTotalTokens != 1342 {
		t.Fatalf("next agent call must keep tool usage in run totals: %#v", stream.pending)
	}
}

func TestToolCharacterUsageEmitsSnapshotWithoutTokens(t *testing.T) {
	stream := &llmRunStream{
		ctx:     context.Background(),
		engine:  &LLMAgentEngine{tools: modelUsageToolExecutor{}},
		session: contracts.QuerySession{RunID: "run_speech_usage", ChatID: "chat_speech_usage"},
		model:   models.ModelDefinition{Key: "chat-model", ContextWindow: 128000},
		execCtx: &contracts.ExecutionContext{
			StartedAt: time.Now(),
			Budget:    contracts.Budget{Tool: contracts.RetryPolicy{MaxCalls: 10}},
		},
	}
	stream.commitUsage(&openAIUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	stream.pending = nil

	if err := stream.startToolCallBatch([]*preparedToolInvocation{{toolID: "tool_speech", toolName: "speech_generate", args: map[string]any{"characters": true}}}); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	var snapshots []contracts.DeltaUsageSnapshot
	for stream.activeToolBatch != nil {
		if err := stream.consumeActiveToolBatch(); err != nil {
			t.Fatalf("consume batch: %v", err)
		}
		for _, delta := range stream.pending {
			if snapshot, ok := delta.(contracts.DeltaUsageSnapshot); ok {
				snapshots = append(snapshots, snapshot)
			}
		}
		stream.pending = nil
	}
	if len(snapshots) != 1 {
		t.Fatalf("character-billed usage must emit one snapshot, got %#v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.Tool == nil || snapshot.Tool.Characters != 42 || snapshot.Tool.ModelKey != "speech-model" || snapshot.Tool.Operation != "speech.generation" {
		t.Fatalf("unexpected tool marker %#v", snapshot.Tool)
	}
	if snapshot.LLMReturnUpstreamRequestID != "trace-1" {
		t.Fatalf("upstream request id = %q", snapshot.LLMReturnUpstreamRequestID)
	}
	if snapshot.LLMReturnTotalTokens != 0 || snapshot.RunTotalTokens != 120 {
		t.Fatalf("characters must not change token totals: %#v", snapshot)
	}
}
