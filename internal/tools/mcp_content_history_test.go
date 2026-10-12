package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/stream"
)

// Exercise the real result adapter, stream mapper, JSONL writer, model-history
// loader and cold replay together. The MCP boundary is a fake, not a live API.
func TestMCPContentBlocksSurviveStreamPersistenceAndHistory(t *testing.T) {
	for _, kind := range []string{"structured", "text", "error"} {
		t.Run(kind, func(t *testing.T) {
			blocks := []any{
				map[string]any{"type": "text", "text": "first note"},
				map[string]any{"type": "resource_link", "uri": "mcp://docs/source", "name": "source", "_meta": map[string]any{"revision": "v2"}},
				map[string]any{"type": "text", "text": "second note"},
			}
			payload := map[string]any{"content": blocks}
			if kind == "structured" {
				payload["structuredContent"] = map[string]any{"count": float64(2), "contentBlocks": "business-owned"}
			}
			if kind == "error" {
				payload["isError"] = true
				payload["structuredContent"] = map[string]any{"code": "version_conflict", "message": "read current version"}
			}
			const chatID, runID, toolID, toolName = "mcp-chat", "mcp-run", "mcp-call", "mcp_remote_search"
			def := api.ToolDetailResponse{Name: toolName, Meta: map[string]any{"sourceType": "mcp", "serverKey": "fake"}}
			router := mustNewToolRouter(t, stubBackendToolExecutor{}, outputSchemaMCPClient{payload: payload}, outputSchemaToolCatalog{def: def}, nil)
			result, err := router.Invoke(context.Background(), toolName, nil, &ExecutionContext{})
			if err != nil {
				t.Fatal(err)
			}

			root := t.TempDir()
			store, err := chat.NewFileStoreAtStartup(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, _, err := store.EnsureChat(chatID, "agent", "query remote resources"); err != nil {
				t.Fatal(err)
			}
			startedAt := time.Now().UnixMilli()
			if err := store.OnRunStarted(chat.RunStart{ChatID: chatID, RunID: runID, AgentKey: "agent", StartedAtMillis: startedAt}); err != nil {
				t.Fatal(err)
			}
			writer := chat.NewStepWriter(store, chatID, runID, "GENERAL")
			assembler := stream.NewAssembler(stream.StreamRequest{RunID: runID, ChatID: chatID, AgentKey: "agent", Message: "query remote resources", StartedAtMillis: startedAt})
			for _, emission := range assembler.BootstrapEmissions() {
				writer.OnEvent(emission.Event.Data())
			}
			mapper := llm.NewDeltaMapper(runID, chatID, Budget{}, nil, nil)
			var liveResult stream.EventData
			for _, delta := range []AgentDelta{
				DeltaToolCall{ID: toolID, Name: toolName, ArgsDelta: "{}"},
				DeltaToolEnd{ToolIDs: []string{toolID}},
				DeltaToolResult{ToolID: toolID, ToolName: toolName, Result: result},
			} {
				for _, input := range mapper.Map(delta) {
					for _, emission := range assembler.ConsumeEmissions(input) {
						data := emission.Event.Data()
						writer.OnEvent(data)
						if data.Type == "tool.result" {
							liveResult = data
						}
					}
				}
			}
			writer.Flush()
			if err := writer.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(liveResult.Value("contentBlocks"), blocks) || liveResult.String("modelOutput") != result.Output || liveResult.String("toolId") != toolID {
				t.Fatalf("live result lost content or call identity: %#v", liveResult)
			}
			if kind == "structured" && !reflect.DeepEqual(liveResult.Value("result"), payload["structuredContent"]) {
				t.Fatalf("business result changed in the live stream: %#v", liveResult.Value("result"))
			}
			if err := store.OnRunCompleted(chat.RunCompletion{ChatID: chatID, RunID: runID, AgentKey: "agent", AssistantText: "done", FinishReason: "complete", StartedAtMillis: startedAt, UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = chat.NewFileStoreAtStartup(root)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := store.LoadRawMessages(chatID, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range messages {
				if message["role"] != "tool" {
					continue
				}
				found = true
				if message["tool_call_id"] != toolID || message["content"] != result.Output || message["name"] != toolName {
					t.Fatalf("restored model history differs from the original result: %#v", message)
				}
				if message["contentBlocks"] != nil || message["_modelOutput"] != nil {
					t.Fatal("application-only fields entered the model message")
				}
			}
			if !found {
				t.Fatal("missing restored tool message")
			}
			detail, err := store.LoadChat(chatID)
			if err != nil {
				t.Fatal(err)
			}
			found = false
			for _, event := range detail.Events {
				if event.Type != "tool.result" {
					continue
				}
				found = true
				if MarshalJSON(event.Value("contentBlocks")) != MarshalJSON(blocks) || event.String("modelOutput") != result.Output || event.String("toolId") != toolID {
					t.Fatalf("cold replay lost content or call identity: %#v", event)
				}
				if kind == "structured" {
					var business map[string]any
					if json.Unmarshal([]byte(event.String("result")), &business) != nil || !reflect.DeepEqual(business, payload["structuredContent"]) {
						t.Fatalf("cold replay changed business result: %#v", event)
					}
				}
			}
			if !found {
				t.Fatal("missing replayed tool result")
			}
		})
	}
}
