package chat

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestBuildLLMChatFromJSONLUsesSystemFingerprint(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-jsonl"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	oldSystem := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:old",
		SystemMessage: map[string]any{"role": "system", "content": "old system"},
		Tools: []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "old_tool",
				"description": "old",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
		Model: map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 0},
	}
	newSystem := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:new",
		SystemMessage: map[string]any{"role": "system", "content": "new system"},
		Tools: []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "new_tool",
				"description": "new",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
		Model: map[string]any{
			"key":         "new-model",
			"id":          "new-model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 1},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Systems:   []QueryLineSystemInit{oldSystem, newSystem},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:old"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if chat.Legacy {
		t.Fatalf("did not expect legacy chat: %#v", chat)
	}
	if got := chat.Messages[0]["content"]; got != "old system" {
		t.Fatalf("expected old system message, got %#v", chat.Messages)
	}
	if got := chat.Messages[1]["content"]; got != "hello" {
		t.Fatalf("expected query message, got %#v", chat.Messages)
	}
	if len(chat.Tools) != 1 {
		t.Fatalf("expected one tool, got %#v", chat.Tools)
	}
	fn, _ := chat.Tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "old_tool" {
		t.Fatalf("expected old tool from fingerprinted systemRef, got %#v", chat.Tools)
	}
	if chat.ToolChoice != "auto" {
		t.Fatalf("tool choice = %q", chat.ToolChoice)
	}
	if chat.Model["id"] != "model-id" {
		t.Fatalf("model snapshot not restored: %#v", chat.Model)
	}
	if chat.RequestOptions["temperature"] != float64(0) {
		t.Fatalf("request options not restored: %#v", chat.RequestOptions)
	}
}

func TestBuildLLMChatFromJSONLAppendsInputMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-input"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "original"},
		Messages:  []map[string]any{{"role": "user", "content": "original"}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "first answer"}},
		}},
	}); err != nil {
		t.Fatalf("append first step: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypePlanExecute,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 3,
		Stage:     "execute",
		Seq:       2,
		System: map[string]any{
			"systemMessage": map[string]any{"role": "system", "content": "execute system"},
			"tools":         []any{},
		},
		InputMessages: []map[string]any{{"role": "user", "content": "execute task"}},
		ToolChoice:    "",
		RequestOptions: map[string]any{
			"stream": true,
		},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "execute answer"}},
		}},
	}); err != nil {
		t.Fatalf("append execute step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Stage: "execute", Seq: 2})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if got := chat.Messages[0]["content"]; got != "execute system" {
		t.Fatalf("expected inline execute system, got %#v", chat.Messages)
	}
	if got := chat.Messages[len(chat.Messages)-1]["content"]; got != "execute task" {
		t.Fatalf("expected input message appended, got %#v", chat.Messages)
	}
	for _, msg := range chat.Messages {
		if msg["content"] == "execute answer" {
			t.Fatalf("target assistant response must not be part of request messages: %#v", chat.Messages)
		}
	}
}

func TestStepWriterKeepsLLMRequestProfileOutOfStepLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-request"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, chatID, "run-1", "REACT")
	writer.SetPendingSystemInits([]QueryLineSystemInit{{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:system",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 0},
	}})
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello"}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{
		"role":    "user",
		"message": "hello",
		"runId":   "run-1",
		"chatId":  chatID,
	}).Data())
	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{
		"runId":  "run-1",
		"chatId": chatID,
		"model": map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		"systemRef":      map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"},
		"toolChoice":     "auto",
		"requestOptions": map[string]any{"stream": true, "temperature": 0},
		"inputMessages":  []any{map[string]any{"role": "user", "content": "internal"}},
	}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "answer",
	}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected query and step lines, got %#v", lines)
	}
	query := lines[0]
	systems, _ := query["systems"].([]any)
	if len(systems) != 1 {
		t.Fatalf("expected query system profile, got %#v", query)
	}
	profile, _ := systems[0].(map[string]any)
	if profile["toolChoice"] != "auto" {
		t.Fatalf("expected query system toolChoice, got %#v", profile)
	}
	model, _ := profile["model"].(map[string]any)
	if model["id"] != "model-id" {
		t.Fatalf("expected query system model snapshot, got %#v", profile)
	}
	options, _ := profile["requestOptions"].(map[string]any)
	if options["temperature"] != float64(0) {
		t.Fatalf("expected query system request options, got %#v", profile)
	}
	step := lines[1]
	if _, ok := step["toolChoice"]; ok {
		t.Fatalf("did not expect step toolChoice, got %#v", step)
	}
	if _, ok := step["model"]; ok {
		t.Fatalf("did not expect step model snapshot, got %#v", step)
	}
	if _, ok := step["requestOptions"]; ok {
		t.Fatalf("did not expect step request options, got %#v", step)
	}
	inputMessages, _ := step["inputMessages"].([]any)
	if len(inputMessages) != 1 {
		t.Fatalf("expected input messages, got %#v", step)
	}
}
