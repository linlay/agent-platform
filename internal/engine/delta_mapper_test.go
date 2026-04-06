package engine

import (
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/stream"
)

func TestDeltaMapperAssignsContentIDsAndToolLifecycleInputs(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", stubToolLookup{
		"_datetime_": api.ToolDetailResponse{
			Name:        "_datetime_",
			Label:       "Date Time",
			Description: "datetime tool",
			Meta:        map[string]any{"kind": "backend"},
		},
	})

	contentInputs := mapper.Map(DeltaContent{Text: "hello"})
	content, ok := contentInputs[0].(stream.ContentDelta)
	if !ok {
		t.Fatalf("expected ContentDelta, got %#v", contentInputs)
	}
	if content.ContentID != "run_1_c_1" {
		t.Fatalf("unexpected content id: %#v", content)
	}

	toolInputs := mapper.Map(DeltaToolCall{
		Index:     0,
		ID:        "tool_1",
		Name:      "_datetime_",
		ArgsDelta: "{",
	})
	toolArgs, ok := toolInputs[0].(stream.ToolArgs)
	if !ok {
		t.Fatalf("expected ToolArgs, got %#v", toolInputs)
	}
	if toolArgs.ChunkIndex != 0 || toolArgs.ToolType != "backend" {
		t.Fatalf("unexpected tool args mapping: %#v", toolArgs)
	}

	secondChunk := mapper.Map(DeltaToolCall{
		Index:     0,
		Name:      "_datetime_",
		ArgsDelta: "}",
	})
	toolArgs, ok = secondChunk[0].(stream.ToolArgs)
	if !ok || toolArgs.ChunkIndex != 1 {
		t.Fatalf("expected second tool chunk index 1, got %#v", secondChunk)
	}

	endInputs := mapper.Map(DeltaToolEnd{ToolIDs: []string{"tool_1"}})
	if _, ok := endInputs[0].(stream.ToolEnd); !ok {
		t.Fatalf("expected ToolEnd, got %#v", endInputs)
	}

	resultInputs := mapper.Map(DeltaToolResult{
		ToolID:   "tool_1",
		ToolName: "_datetime_",
		Result:   ToolExecutionResult{Structured: map[string]any{"ok": true}},
	})
	if _, ok := resultInputs[0].(stream.ToolResult); !ok {
		t.Fatalf("expected ToolResult, got %#v", resultInputs)
	}
}

func TestDeltaMapperMapsActionTools(t *testing.T) {
	mapper := NewDeltaMapper("run_1", "chat_1", stubToolLookup{
		"confirm_dialog": api.ToolDetailResponse{
			Name:        "confirm_dialog",
			Description: "confirm",
			Meta:        map[string]any{"kind": "action"},
		},
	})

	inputs := mapper.Map(DeltaToolCall{
		Index:     0,
		ID:        "action_1",
		Name:      "confirm_dialog",
		ArgsDelta: "{\"ok\":true}",
	})
	if _, ok := inputs[0].(stream.ActionArgs); !ok {
		t.Fatalf("expected ActionArgs, got %#v", inputs)
	}

	endInputs := mapper.Map(DeltaToolEnd{ToolIDs: []string{"action_1"}})
	if _, ok := endInputs[0].(stream.ActionEnd); !ok {
		t.Fatalf("expected ActionEnd, got %#v", endInputs)
	}
}

type stubToolLookup map[string]api.ToolDetailResponse

func (s stubToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	value, ok := s[name]
	return value, ok
}
