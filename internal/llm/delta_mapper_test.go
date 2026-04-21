package llm

import (
	"testing"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/stream"
)

type stubToolLookup map[string]api.ToolDetailResponse

func (s stubToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	tool, ok := s[name]
	return tool, ok
}

func TestDeltaMapper_QuestionInitialAwaitAskComesFromFrontendHandler(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_question_",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select","options":[{"label":"Weekend"}]}]}`,
	})
	if len(inputs) != 1 {
		t.Fatalf("expected one mapped input, got %#v", inputs)
	}
	args, ok := inputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input, got %#v", inputs[0])
	}
	if args.AwaitAsk == nil {
		t.Fatalf("expected initial await ask, got %#v", args)
	}
	if args.AwaitAsk.Mode != "question" || args.AwaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", args.AwaitAsk)
	}
}

func TestDeltaMapper_InvalidQuestionArgsDoNotEmitInitialAwaitAsk(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_question_",
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
}

func TestDeltaMapper_QuestionChunkedArgsEmitStandaloneAwaitAskWhenPayloadBecomesValid(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	firstInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_question_",
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
		Name:      "_ask_user_question_",
		ArgsDelta: `"options":[{"label":"Weekend"}]}]}`,
	})
	if len(secondInputs) != 2 {
		t.Fatalf("expected standalone await ask and tool args on second chunk, got %#v", secondInputs)
	}
	awaitAsk, ok := secondInputs[0].(stream.AwaitAsk)
	if !ok {
		t.Fatalf("expected AwaitAsk input first, got %#v", secondInputs[0])
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	secondArgs, ok := secondInputs[1].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs input second, got %#v", secondInputs[1])
	}
	if secondArgs.AwaitAsk != nil {
		t.Fatalf("did not expect inline await ask on second chunk, got %#v", secondArgs.AwaitAsk)
	}
	if secondArgs.ChunkIndex != 1 {
		t.Fatalf("expected second chunk index to remain 1, got %#v", secondArgs)
	}
	if len(mapper.toolArgBuffers) != 0 {
		t.Fatalf("expected buffered tool arg payload to be cleared, got %#v", mapper.toolArgBuffers)
	}
}

func TestDeltaMapper_InvalidChunkedQuestionArgsDoNotEmitStandaloneAwaitAsk(t *testing.T) {
	mapper := newQuestionDeltaMapper()

	firstInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_question_",
		ArgsDelta: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select"`,
	})
	if len(firstInputs) != 1 {
		t.Fatalf("expected one mapped input for first chunk, got %#v", firstInputs)
	}

	secondInputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_question_",
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
}

func newQuestionDeltaMapper() *DeltaMapper {
	tools := stubToolLookup{
		"_ask_user_question_": {
			Name: "_ask_user_question_",
			Meta: map[string]any{
				"kind":          "frontend",
				"clientVisible": true,
			},
		},
	}
	return NewDeltaMapper("run_1", "chat_1", 5000, tools, frontendtools.NewDefaultRegistry())
}

func TestDeltaMapperCloneIsolatedStartsFreshState(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", 5000, stubToolLookup{}, frontendtools.NewDefaultRegistry())

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
