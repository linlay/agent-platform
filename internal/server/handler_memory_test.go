package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/memory"
)

func TestHandleLearnStoresObservationFromLatestRun(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "please fix the memory bug"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-1", chat.QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 100,
		Query: map[string]any{
			"message": "please fix the memory bug",
			"role":    "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-1", chat.StepLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 200,
		Type:      "react",
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: "Fixed the memory bug and tightened scope handling."}},
				ToolCalls: []chat.StoredToolCall{
					{
						ID:   "tool-1",
						Type: "function",
						Function: chat.StoredFunction{
							Name:      "_memory_search_",
							Arguments: "{}",
						},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-1",
		RunID:           "run-1",
		AssistantText:   "Fixed the memory bug and tightened scope handling.",
		UpdatedAtMillis: 300,
	}); err != nil {
		t.Fatalf("on run completed: %v", err)
	}

	server := &Server{deps: Dependencies{
		Chats:  chats,
		Memory: memories,
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/learn", bytes.NewBufferString(`{"requestId":"learn-1","chatId":"chat-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleLearn(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.LearnResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Data.Accepted || resp.Data.ObservationCount != 1 {
		t.Fatalf("unexpected learn response: %#v", resp.Data)
	}
	record, err := memories.ReadDetail("agent-a", resp.Data.Stored[0].ID)
	if err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if record == nil || record.Kind != memory.KindObservation || record.Category != "bugfix" {
		t.Fatalf("unexpected learned memory: %#v", record)
	}
}

func TestHandleRememberReturnsStoredMemoryFromChatStore(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-remember", "agent-a", "team-1", "记住这个答案"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-remember", chat.QueryLine{
		ChatID:    "chat-remember",
		RunID:     "run-remember",
		UpdatedAt: 100,
		Query: map[string]any{
			"message": "记住这个答案",
			"role":    "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-remember", chat.StepLine{
		ChatID:    "chat-remember",
		RunID:     "run-remember",
		UpdatedAt: 200,
		Type:      "react",
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: "这是需要被记住的答案。"}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-remember",
		RunID:           "run-remember",
		AssistantText:   "这是需要被记住的答案。",
		UpdatedAtMillis: 300,
	}); err != nil {
		t.Fatalf("on run completed: %v", err)
	}

	server := &Server{deps: Dependencies{
		Chats:  chats,
		Memory: memories,
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/remember", bytes.NewBufferString(`{"requestId":"remember-1","chatId":"chat-remember"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleRemember(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.RememberResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Data.Accepted || resp.Data.MemoryCount != 1 {
		t.Fatalf("unexpected remember response: %#v", resp.Data)
	}
	record, err := memories.ReadDetail("agent-a", resp.Data.Stored[0].ID)
	if err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if record == nil || record.Kind != memory.KindFact || record.Category != "remember" {
		t.Fatalf("unexpected remembered memory: %#v", record)
	}
}
