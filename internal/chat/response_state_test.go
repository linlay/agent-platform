package chat

import (
	"agent-platform/internal/modelcontent"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestResponsesJSONLCommitAndToolContinuation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err = store.EnsureChat("c", "agent", "", "hello"); err != nil {
		t.Fatal(err)
	}
	writer := NewStepWriter(store, "c", "r", "REACT")
	ts := testEpochMillis(1)
	writer.lastTimestamp = ts
	writer.modelTurnCommitRequired = true
	writer.pendingModelKey = "luna"
	writer.messages = []StoredMessage{{Role: "assistant", Ts: &ts, ToolCalls: []StoredToolCall{{ID: "call1", Type: "function", Function: StoredFunction{Name: "file_read", Arguments: `{"path":"README.md"}`}}}}}
	parts := []modelcontent.ReasoningPart{{Type: "encrypted_text", ID: "rs1", EncryptedText: "secret-cipher", Summary: json.RawMessage(`[]`)}}
	writer.SetModelResponse("", "resp1", parts)
	writer.CommitModelTurn("", 1)
	writer.Flush()
	writer.lastTimestamp = testEpochMillis(2)
	writer.messages = []StoredMessage{{Role: "tool", Ts: &ts, ToolCallID: "call1", Content: textContent("Go")}}
	writer.Flush()
	writer.lastTimestamp = testEpochMillis(3)
	writer.modelTurnCommitRequired = true
	writer.messages = []StoredMessage{{Role: "assistant", Ts: &ts, Content: textContent("answer")}}
	writer.SetModelResponse("", "resp2", parts)
	writer.CommitModelTurn("", 2)
	writer.Flush()
	if writer.persistenceErr != nil {
		t.Fatal(writer.persistenceErr)
	}
	data, err := os.ReadFile(store.chatJSONLPath("c"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var line map[string]any
		if err = json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		if lineIsStep(line) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("lines=%s", data)
	}
	if lines[0]["responseId"] != "resp1" || lines[1]["responseId"] != nil || lines[1]["_type"] != "react-tool" || lines[1]["seq"] != lines[0]["seq"] || lines[2]["responseId"] != "resp2" {
		t.Fatalf("bad grouping %s", data)
	}
	// Reload from the persisted file, without using the writer's memory.
	raw, err := loadRawMessagesFromPath(store.chatJSONLPath("c"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || !hasEncryptedReasoning(raw[0]["reasoning_content"]) {
		t.Fatalf("lost encrypted history %#v", raw)
	}
	if strings.Contains(extractTextFromContent(raw[0]["reasoning_content"]), "secret-cipher") {
		t.Fatal("cipher exposed as text")
	}
	if strings.Contains(renderMessagesForCompact(raw, 0), "secret-cipher") {
		t.Fatal("cipher entered summary")
	}
	writer.lastTimestamp = testEpochMillis(4)
	writer.modelTurnCommitRequired = true
	writer.messages = []StoredMessage{{Role: "assistant", Ts: &ts, Content: textContent("discard")}}
	writer.SetModelResponse("", "resp-discard", parts)
	writer.DiscardModelTurn("", 3, false)
	writer.Flush()
	after, _ := os.ReadFile(store.chatJSONLPath("c"))
	if string(after) != string(data) {
		t.Fatal("discarded response persisted")
	}
}
func TestResponsesL1ProtectsRequiredReasoning(t *testing.T) {
	cipher := []any{map[string]any{"type": "encrypted_text", "id": "rs", "encrypted_text": strings.Repeat("cipher", 10000), "summary": []any{}}}
	call := func(name string) map[string]any {
		return map[string]any{"role": "assistant", "reasoning_content": cipher, "tool_calls": []any{map[string]any{"id": "call", "function": map[string]any{"name": name, "arguments": "{}"}}}}
	}
	// Unfinished interaction must not lose state, regardless of readable text.
	p := ProjectL1([]map[string]any{call("file_read")}, 0, -1, -1, false)
	if !hasEncryptedReasoning(p.Messages[0]["reasoning_content"]) {
		t.Fatal("pending reasoning removed")
	}
	messages := []map[string]any{call("file_read"), {"role": "tool", "tool_call_id": "call", "content": "result"}}
	p = ProjectL1(messages, 0, -1, -1, false)
	if p.ToolsCleared != 1 || p.ReasoningCleared != 1 || p.Messages[0] != nil || p.Messages[1] != nil {
		t.Fatalf("tool group not removed atomically %#v", p)
	}
	if EstimateRawMessageTokens(messages) > 500 {
		t.Fatal("ciphertext counted as text tokens")
	}
	if !hasEncryptedReasoning(messages[0]["reasoning_content"]) {
		t.Fatal("projection mutated original")
	}
}
func TestResponsesSchemaAndLegacyTextShape(t *testing.T) {
	data, _ := json.Marshal(ContentPart{Type: "text"})
	if string(data) != `{"type":"text","text":""}` {
		t.Fatal(string(data))
	}
	line := map[string]any{"_type": "react-tool", "responseId": "resp"}
	if validateResponseState(line) == nil {
		t.Fatal("responseId accepted on tool result")
	}
	line = map[string]any{"_type": "react", "messages": []any{map[string]any{"role": "assistant", "reasoning_content": []any{map[string]any{"type": "encrypted_text", "id": "rs", "text": "cipher"}}}}}
	if validateResponseState(line) == nil {
		t.Fatal("cipher accepted in text property")
	}
}

func TestResponseMetadataAttachesAfterCommitSnapshot(t *testing.T) {
	// The dispatcher may deliver the final assistant snapshot after ModelTurnCommit.
	parts := []ContentPart{{Type: "encrypted_text", ID: "rs", EncryptedText: "cipher", Summary: json.RawMessage(`[]`)}}
	ts := int64(42)
	w := &StepWriter{modelTurnCommitRequired: true}
	w.SetModelResponse("", "resp", parts)
	w.CommitModelTurn("", 1)
	w.messages = []StoredMessage{{Role: "assistant", Ts: &ts, Content: textContent("late snapshot")}}
	messages := attachResponseReasoning(w.messages, w.encryptedReasoning, w.responseID, ts)
	if len(messages) != 1 || len(messages[0].ReasoningContent) != 1 || messages[0].Content[0].Text != "late snapshot" {
		t.Fatal(messages)
	}
	if len(w.messages[0].ReasoningContent) != 0 {
		t.Fatal("snapshot mutated")
	}
	empty := attachResponseReasoning(nil, parts, "resp", ts)
	if len(empty) != 1 || empty[0].Role != "assistant" {
		t.Fatal(empty)
	}
}

func TestResponsesDocumentJSONL(t *testing.T) {
	data, err := os.ReadFile("../../docs/Responses协议.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(data), "```jsonl\n")
	if len(parts) != 2 {
		t.Fatal("missing JSONL example")
	}
	example := strings.SplitN(parts[1], "```", 2)[0]
	if err = ValidateJSONLContent(example, "Responses docs example"); err != nil {
		t.Fatal(err)
	}
}
