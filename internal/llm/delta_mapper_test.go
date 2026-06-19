package llm

import (
	"testing"

	"agent-platform/internal/api"
	contracts "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/stream"
)

type stubToolLookup map[string]api.ToolDetailResponse

func (s stubToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	tool, ok := s[name]
	return tool, ok
}

func TestDeltaMapper_QuestionAwaitAskEmitsAfterToolEnd(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select","options":[{"label":"Weekend"}]}]}`,
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	args, ok := inputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", inputs[0])
	}
	if args.AwaitAsk != nil {
		t.Fatalf("did not expect await ask before tool end, got %#v", args.AwaitAsk)
	}

	endInputs := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"tool_1"}})
	if len(endInputs) != 2 {
		t.Fatalf("expected tool end followed by await ask, got %#v", endInputs)
	}
	if _, ok := endInputs[0].(stream.ToolEnd); !ok {
		t.Fatalf("expected ToolEnd first, got %#v", endInputs[0])
	}
	awaitAsk, ok := endInputs[1].(stream.AwaitAsk)
	if !ok {
		t.Fatalf("expected AwaitAsk second, got %#v", endInputs[1])
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
}

func TestDeltaMapper_InvalidQuestionArgsDoNotEmitInitialAwaitAsk(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select"}]}`,
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	args, ok := inputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", inputs[0])
	}
	if args.AwaitAsk != nil {
		t.Fatalf("did not expect initial await ask, got %#v", args.AwaitAsk)
	}

	endInputs := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"tool_1"}})
	if len(endInputs) != 1 {
		t.Fatalf("expected only tool end for invalid args, got %#v", endInputs)
	}
	if _, ok := endInputs[0].(stream.ToolEnd); !ok {
		t.Fatalf("expected ToolEnd input, got %#v", endInputs[0])
	}
}

func TestDeltaMapper_QuestionChunkedArgsEmitAwaitAskAfterToolEnd(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	firstInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select",`,
	})
	if len(firstInputs) != 1 {
		t.Fatalf("expected one mapped input for first chunk, got %#v", firstInputs)
	}
	firstArgs, ok := firstInputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input for first chunk, got %#v", firstInputs[0])
	}
	if firstArgs.AwaitAsk != nil {
		t.Fatalf("did not expect initial await ask on first chunk, got %#v", firstArgs.AwaitAsk)
	}
	if len(mapper.toolArgBuffers) != 1 {
		t.Fatalf("expected one buffered tool arg payload after first chunk, got %#v", mapper.toolArgBuffers)
	}

	secondInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `"options":[{"label":"Weekend"}]}]}`,
	})
	if len(secondInputs) != 1 {
		t.Fatalf("expected one mapped input for second chunk, got %#v", secondInputs)
	}
	secondArgs, ok := secondInputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", secondInputs[0])
	}
	if secondArgs.AwaitAsk != nil {
		t.Fatalf("did not expect await ask on second chunk, got %#v", secondArgs.AwaitAsk)
	}
	if secondArgs.ChunkIndex != 1 {
		t.Fatalf("expected second chunk index to remain 1, got %#v", secondArgs)
	}
	if len(mapper.toolArgBuffers) != 0 {
		t.Fatalf("expected buffered tool arg payload to be cleared, got %#v", mapper.toolArgBuffers)
	}

	endInputs := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"tool_1"}})
	if len(endInputs) != 2 {
		t.Fatalf("expected tool end followed by await ask, got %#v", endInputs)
	}
	if _, ok := endInputs[0].(stream.ToolEnd); !ok {
		t.Fatalf("expected ToolEnd first, got %#v", endInputs[0])
	}
	awaitAsk, ok := endInputs[1].(stream.AwaitAsk)
	if !ok {
		t.Fatalf("expected AwaitAsk second, got %#v", endInputs[1])
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
}

func TestDeltaMapper_InvalidChunkedQuestionArgsDoNotEmitStandaloneAwaitAsk(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	firstInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select"`,
	})
	if len(firstInputs) != 1 {
		t.Fatalf("expected one mapped input for first chunk, got %#v", firstInputs)
	}

	secondInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "ask_user_question",
		ArgsDelta: `}]}`,
	})
	if len(secondInputs) != 1 {
		t.Fatalf("expected one mapped input for invalid second chunk, got %#v", secondInputs)
	}
	secondArgs, ok := secondInputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", secondInputs[0])
	}
	if secondArgs.AwaitAsk != nil {
		t.Fatalf("did not expect await ask for invalid chunked args, got %#v", secondArgs.AwaitAsk)
	}
	if len(mapper.toolArgBuffers) != 0 {
		t.Fatalf("expected buffered tool arg payload to be cleared after validation failure, got %#v", mapper.toolArgBuffers)
	}

	endInputs := mapper.Map(contracts.DeltaToolEnd{ToolIDs: []string{"tool_1"}})
	if len(endInputs) != 1 {
		t.Fatalf("expected only tool end for invalid chunked args, got %#v", endInputs)
	}
	if _, ok := endInputs[0].(stream.ToolEnd); !ok {
		t.Fatalf("expected ToolEnd input, got %#v", endInputs[0])
	}
}

func TestDeltaMapper_GenericFrontendToolEmitsFormAwaitAsk(t *testing.T) {
	tools := stubToolLookup{
		"leave_form": {
			Name:  "leave_form",
			Label: "Leave Form",
			Meta: map[string]any{
				"kind":         "frontend",
				"viewportType": "html",
				"viewportKey":  "leave_form",
			},
		},
	}
	mapper := NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, tools, frontendtools.NewDefaultRegistry())

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "leave_form",
		ArgsDelta: `{"employeeName":"Lin","reason":"family"}`,
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	args, ok := inputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", inputs[0])
	}
	if args.AwaitAsk == nil {
		t.Fatalf("expected generic frontend await ask, got %#v", args)
	}
	if args.AwaitAsk.Mode != "form" || args.AwaitAsk.ViewportType != "html" || args.AwaitAsk.ViewportKey != "leave_form" {
		t.Fatalf("unexpected await ask %#v", args.AwaitAsk)
	}
	if len(args.AwaitAsk.Forms) != 1 {
		t.Fatalf("expected one form, got %#v", args.AwaitAsk.Forms)
	}
}

func TestDeltaMapper_DebugLLMCall(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", contracts.Budget{}, nil, nil)

	inputs := mapper.Map(contracts.DeltaDebugLLMCall{
		TaskID:                          "task_1",
		ChatID:                          "chat_1",
		ProviderKey:                     "mock",
		ProviderEndpoint:                "https://api.example.test/v1/chat/completions",
		ModelKey:                        "mock-model",
		ModelID:                         "mock-model-id",
		ReasoningEffort:                 "HIGH",
		Status:                          "ok",
		RunSeq:                          1,
		TraceFile:                       "llm/run_1_001.json",
		TraceURL:                        "/api/resource?file=llm%2Frun_1_001.json",
		SystemRef:                       map[string]any{"cacheKey": "react:main"},
		ContextWindow:                   128000,
		CurrentContextSize:              10,
		EstimatedNextCallSize:           20,
		LLMReturnPromptTokens:           7,
		LLMReturnCompletionTokens:       3,
		LLMReturnTotalTokens:            10,
		LLMReturnLLMChatCompletionCount: 1,
		RunPromptTokens:                 7,
		RunCompletionTokens:             3,
		RunTotalTokens:                  10,
		RunLLMChatCompletionCount:       1,
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	call, ok := inputs[0].(stream.InputDebugLLMCall)
	if !ok {
		t.Fatalf("expected InputDebugLLMCall, got %#v", inputs[0])
	}
	if call.TaskID != "task_1" || call.TraceFile != "llm/run_1_001.json" || call.RunSeq != 1 || call.Status != "ok" {
		t.Fatalf("unexpected mapped debug llm call %#v", call)
	}
	if call.SystemRef["cacheKey"] != "react:main" {
		t.Fatalf("expected cloned systemRef, got %#v", call.SystemRef)
	}
}

func newQuestionDeltaMapper() *DeltaMapper {
	tools := stubToolLookup{
		"ask_user_question": {
			Name: "ask_user_question",
			Meta: map[string]any{
				"kind":          "frontend",
				"clientVisible": false,
			},
		},
	}
	return NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, tools, frontendtools.NewDefaultRegistry())
}

func TestDeltaMapperCloneIsolatedStartsFreshState(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, stubToolLookup{}, frontendtools.NewDefaultRegistry())

	first := mapper.Map(contracts.DeltaContent{Text: "root"})
	content, ok := first[0].(stream.ContentDelta)
	if !ok || content.ContentID != "run_1_c_1" {
		t.Fatalf("expected first content id run_1_c_1, got %#v", first)
	}

	child := mapper.CloneIsolated("task_1", "chat_1")
	if child == nil {
		t.Fatal("expected isolated mapper clone")
	}
	second := child.Map(contracts.DeltaContent{Text: "child"})
	content, ok = second[0].(stream.ContentDelta)
	if !ok || content.ContentID != "task_1_c_1" {
		t.Fatalf("expected child content id task_1_c_1, got %#v", second)
	}

	third := mapper.Map(contracts.DeltaContent{Text: "root again"})
	content, ok = third[0].(stream.ContentDelta)
	if !ok || content.ContentID != "run_1_c_1" {
		t.Fatalf("expected original mapper state to remain unchanged, got %#v", third)
	}
}

func TestDeltaMapper_ArtifactPublishPreservesBatchPayload(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, stubToolLookup{}, frontendtools.NewDefaultRegistry())

	inputs := mapper.Map(contracts.DeltaArtifactPublish{
		ChatID:        "chat_1",
		RunID:         "run_1",
		ArtifactCount: 2,
		Artifacts: []map[string]any{
			{"artifactId": "artifact_1", "name": "report.md"},
			{"artifactId": "artifact_2", "name": "summary.txt"},
		},
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	event, ok := inputs[0].(stream.ArtifactPublish)
	if !ok {
		t.Fatalf("expected ArtifactPublish input, got %#v", inputs[0])
	}
	if event.ArtifactCount != 2 || len(event.Artifacts) != 2 {
		t.Fatalf("unexpected artifact batch %#v", event)
	}
}
