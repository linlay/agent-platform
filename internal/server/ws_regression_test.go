package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

func TestServerSharedHelpersUseCommonChatAndMemoryStores(t *testing.T) {
	server, chats, memories := newServerForHelperTests(t)

	if _, _, err := chats.EnsureChat("chat-1", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-1", chat.QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1001,
		Query: map[string]any{
			"chatId":  "chat-1",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-1", chat.StepLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1002,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "user", Content: []chat.ContentPart{{Type: "text", Text: "hello"}}},
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "answer"}}},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-1",
		RunID:           "run-1",
		AssistantText:   "answer",
		InitialMessage:  "hello",
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage: chat.UsageData{
			PromptTokens:     3,
			CompletionTokens: 5,
			TotalTokens:      8,
		},
	}); err != nil {
		t.Fatalf("persist run completion: %v", err)
	}

	summaries, err := server.listChatSummaries("", "")
	if err != nil {
		t.Fatalf("list chat summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary, got %#v", summaries)
	}
	if summaries[0].LastRunID != "run-1" || summaries[0].Usage == nil || summaries[0].Usage.TotalTokens != 8 {
		t.Fatalf("unexpected chat summary %#v", summaries[0])
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-1", true)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ChatID != "chat-1" || len(detail.Events) == 0 || len(detail.RawMessages) < 2 {
		t.Fatalf("unexpected chat detail %#v", detail)
	}

	rememberResp, err := server.executeRemember(api.RememberRequest{
		RequestID: "req-remember",
		ChatID:    "chat-1",
	})
	if err != nil {
		t.Fatalf("execute remember: %v", err)
	}
	if !rememberResp.Accepted || rememberResp.MemoryCount != 1 {
		t.Fatalf("unexpected remember response %#v", rememberResp)
	}
	matches, err := memories.Search("answer", 10)
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected stored memory, got %#v", matches)
	}
}

func TestLoadChatDetailAndRememberReturnNotFoundAcrossHTTP(t *testing.T) {
	server, _, _ := newServerForHelperTests(t)

	if _, err := server.loadChatDetail(context.Background(), "missing-chat", false); err == nil {
		t.Fatalf("expected loadChatDetail to return not found")
	}
	if _, err := server.executeRemember(api.RememberRequest{RequestID: "req_missing", ChatID: "missing-chat"}); err == nil {
		t.Fatalf("expected executeRemember to return not found")
	}

	chatRec := httptest.NewRecorder()
	server.handleChat(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=missing-chat", nil))
	if chatRec.Code != http.StatusNotFound {
		t.Fatalf("expected HTTP chat 404, got %d: %s", chatRec.Code, chatRec.Body.String())
	}

	rememberReq := httptest.NewRequest(http.MethodPost, "/api/remember", strings.NewReader(`{"requestId":"req_missing","chatId":"missing-chat"}`))
	rememberReq.Header.Set("Content-Type", "application/json")
	rememberRec := httptest.NewRecorder()
	server.handleRemember(rememberRec, rememberReq)
	if rememberRec.Code != http.StatusNotFound {
		t.Fatalf("expected HTTP remember 404, got %d: %s", rememberRec.Code, rememberRec.Body.String())
	}
}

func TestBroadcastDefinitionsStayAlignedAcrossHTTPAndWS(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	handlerQuery := mustReadFile(t, filepath.Join(root, "handler_query.go"))
	handlerChat := mustReadFile(t, filepath.Join(root, "handler_chat.go"))
	wsRoutes := mustReadFile(t, filepath.Join(root, "ws_routes.go"))

	assertContains(t, handlerQuery, `s.broadcast("run.started"`)
	assertContains(t, handlerQuery, `s.broadcast("run.finished"`)
	assertContains(t, handlerQuery, `s.broadcast("chat.created"`)
	assertContains(t, handlerChat, `s.broadcast("chat.read"`)
	assertContains(t, wsRoutes, `s.broadcast("run.started"`)
	assertContains(t, wsRoutes, `s.broadcast("run.finished"`)
	assertContains(t, wsRoutes, `s.broadcast("chat.read"`)
}

func newServerForHelperTests(t *testing.T) (*Server, *chat.FileStore, *memory.FileStore) {
	t.Helper()
	root := t.TempDir()
	chats, err := chat.NewFileStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	server := &Server{
		deps: Dependencies{
			Config: config.Config{},
			Chats:  chats,
			Memory: memories,
		},
		ticketService: NewResourceTicketService(config.ChatImageTokenConfig{}),
	}
	return server, chats, memories
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, text string, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q in file contents", want)
	}
}
