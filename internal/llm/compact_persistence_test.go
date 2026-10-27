package llm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/models"
)

func TestSummaryCommitMatchesProviderToolCallsToStoredCalls(t *testing.T) {
	for _, scenario := range []string{"storage metadata matches", "changed arguments", "split persisted record"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store, err := chat.NewFileStoreAtStartup(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			const id = "chat-summary-tool-metadata"
			timestamp := int64(1792637650183)
			for i, run := range []string{"old", "current"} {
				if err := store.AppendQueryLine(id, chat.QueryLine{
					Type: "query", ChatID: id, RunID: run, UpdatedAt: timestamp,
					Messages: []map[string]any{{"role": "user", "content": run + " instruction", "ts": timestamp}},
				}); err != nil {
					t.Fatal(err)
				}
				if err := store.AppendStepLine(id, chat.StepLine{
					Type: "react", ChatID: id, RunID: run, Seq: i + 1, UpdatedAt: timestamp,
					Messages: []chat.StoredMessage{
						{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: run + " prose"}}, Ts: &timestamp},
						{Role: "assistant", Ts: &timestamp, ToolCalls: []chat.StoredToolCall{{
							ID: run + "-call", Type: "function", ToolID: run + "-internal",
							Function: chat.StoredFunction{Name: "file_read", Arguments: `{"path":"example.txt"}`},
						}}},
					},
				}); err != nil {
					t.Fatal(err)
				}
				if err := store.AppendStepLine(id, chat.StepLine{
					Type: "react-tool", ChatID: id, RunID: run, Seq: i + 1, UpdatedAt: timestamp,
					Messages: []chat.StoredMessage{{Role: "tool", ToolCallID: run + "-call", Content: []chat.ContentPart{{Type: "text", Text: run + " result"}}, Ts: &timestamp}},
				}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := store.LoadRawMessages(id, 20)
			if err != nil {
				t.Fatal(err)
			}
			s := &llmRunStream{model: models.ModelDefinition{ContextWindow: 262144}}
			for _, message := range mergeRawMessagesByMsgID(raw) {
				s.messages = append(s.messages, rawMessageToOpenAI(message, false))
			}
			s.pinnedMessageStart, s.pinnedMessageEnd = 4, 5
			plan := s.buildContextCompactPlan(false)
			covered := modelMessagesToMaps(plan.candidates)
			if len(covered) != 4 || len(plan.retained) != 3 {
				t.Fatalf("unexpected plan: covered=%d retained=%d", len(covered), len(plan.retained))
			}
			if scenario == "changed arguments" {
				for _, message := range covered {
					if calls, ok := message["tool_calls"].([]any); ok {
						calls[0].(map[string]any)["function"].(map[string]any)["arguments"] = `{"path":"different.txt"}`
					}
				}
			}
			if scenario == "split persisted record" {
				// Keep the prose while summarizing the tool call stored beside it.
				plan.retained = append([]openAIMessage{plan.candidates[1]}, plan.retained...)
				covered = append(covered[:1], covered[2:]...)
			}
			messages := append([]map[string]any{{"role": "user", "content": chat.CompactCheckpointSummaryMessage("old summary")}}, modelMessagesToMaps(append(plan.pinned, plan.retained...))...)
			path := filepath.Join(root, id+".jsonl")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = store.AppendRunCompactCheckpoint(id, chat.RunCompactCheckpointLine{Level: "summary", CompactID: "summary", RunID: "current", UpdatedAt: timestamp, Messages: messages, CompactCoveredMessages: covered})
			if scenario != "storage metadata matches" {
				if err == nil {
					t.Fatalf("invalid %s accepted", scenario)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected commit changed source")
				}
				return
			}
			if err != nil {
				t.Fatalf("commit provider messages against stored tool metadata: %v", err)
			}
			reloaded, err := store.LoadRawMessages(id, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded) != 5 || reloaded[0]["content"] != chat.CompactCheckpointSummaryMessage("old summary") {
				t.Fatalf("unexpected reloaded message count=%d", len(reloaded))
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(after, []byte(`"_toolId":"old-internal"`)) || !bytes.Contains(after, []byte(`"_toolId":"current-internal"`)) {
				t.Fatal("original tool metadata lost")
			}
		})
	}
}
