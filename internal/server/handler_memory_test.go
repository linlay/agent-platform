package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/memory"
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
							Name:      "memory_search",
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
		Config: config.Config{
			Memory: config.MemoryConfig{Enabled: true},
		},
		Chats:  chats,
		Memory: memories,
		Registry: queryMemoryRegistry{def: catalog.AgentDefinition{
			Key:           "agent-a",
			Name:          "Agent A",
			ModelKey:      "mock-model",
			MemoryEnabled: true,
		}},
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

func TestHandleLearnReturnsDisabledWhenMemorySystemDisabled(t *testing.T) {
	server := &Server{deps: Dependencies{
		Config: config.Config{
			Memory: config.MemoryConfig{},
		},
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/learn", bytes.NewBufferString(`{"requestId":"learn-1","chatId":"chat-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleLearn(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
