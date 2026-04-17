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
	tools := stubToolLookup{
		"_ask_user_question_": {
			Name: "_ask_user_question_",
			Meta: map[string]any{
				"kind":          "frontend",
				"viewportType":  "builtin",
				"viewportKey":   "confirm_dialog",
				"clientVisible": true,
			},
		},
	}
	mapper := NewDeltaMapper("run_1", "chat_1", 5000, tools, frontendtools.NewDefaultRegistry())

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
	tools := stubToolLookup{
		"_ask_user_question_": {
			Name: "_ask_user_question_",
			Meta: map[string]any{
				"kind":          "frontend",
				"viewportType":  "builtin",
				"viewportKey":   "confirm_dialog",
				"clientVisible": true,
			},
		},
	}
	mapper := NewDeltaMapper("run_1", "chat_1", 5000, tools, frontendtools.NewDefaultRegistry())

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

func TestDeltaMapper_ApprovalDoesNotEmitInitialAwaitAsk(t *testing.T) {
	tools := stubToolLookup{
		"_ask_user_approval_": {
			Name: "_ask_user_approval_",
			Meta: map[string]any{
				"kind":          "frontend",
				"viewportType":  "builtin",
				"viewportKey":   "confirm_dialog",
				"clientVisible": true,
			},
		},
	}
	mapper := NewDeltaMapper("run_1", "chat_1", 5000, tools, frontendtools.NewDefaultRegistry())

	inputs := mapper.Map(contracts.DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_ask_user_approval_",
		ArgsDelta: "{",
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
