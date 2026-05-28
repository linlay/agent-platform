package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
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
			PromptTokens:           3,
			CompletionTokens:       5,
			TotalTokens:            8,
			CachedTokens:           2,
			ReasoningTokens:        4,
			PromptCacheHitTokens:   2,
			PromptCacheMissTokens:  1,
			LlmChatCompletionCount: 1,
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
	if summaries[0].Usage.PromptTokensDetails == nil || summaries[0].Usage.PromptTokensDetails.CacheHitTokens != 2 ||
		summaries[0].Usage.PromptTokensDetails.CacheMissTokens != 1 ||
		summaries[0].Usage.CompletionTokensDetails == nil || summaries[0].Usage.CompletionTokensDetails.ReasoningTokens != 4 ||
		summaries[0].Usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat summary usage, got %#v", summaries[0].Usage)
	}
	if summaries[0].Read.IsRead {
		t.Fatalf("expected completed chat to be unread, got %#v", summaries[0].Read)
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-1", true)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ChatID != "chat-1" || len(detail.Events) == 0 || len(detail.RawMessages) < 2 {
		t.Fatalf("unexpected chat detail %#v", detail)
	}
	if detail.Usage == nil || detail.Usage.LastRun == nil || detail.Usage.Chat == nil {
		t.Fatalf("expected detailed chat detail usage breakdown, got %#v", detail.Usage)
	}
	if detail.Usage.LastRun.PromptTokensDetails == nil || detail.Usage.LastRun.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Usage.LastRun.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Usage.LastRun.CompletionTokensDetails == nil || detail.Usage.LastRun.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Usage.LastRun.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat detail usage, got %#v", detail.Usage)
	}
	if detail.Usage.Chat.PromptTokensDetails == nil || detail.Usage.Chat.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Usage.Chat.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Usage.Chat.CompletionTokensDetails == nil || detail.Usage.Chat.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Usage.Chat.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat cumulative usage, got %#v", detail.Usage)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Usage.PromptTokensDetails == nil || detail.Runs[0].Usage.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Runs[0].Usage.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Runs[0].Usage.CompletionTokensDetails == nil || detail.Runs[0].Usage.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Runs[0].Usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed run summary usage, got %#v", detail.Runs)
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

func TestLoadChatDetailUsageBreakdownSeparatesLastRunFromChatTotal(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)

	if _, _, err := chats.EnsureChat("chat-usage-breakdown", "agent-1", "", "first"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-usage-breakdown",
		RunID:           "run-usage-1",
		InitialMessage:  "first",
		AssistantText:   "first answer",
		UpdatedAtMillis: 1000,
		Usage: chat.UsageData{
			PromptTokens:           10,
			CompletionTokens:       5,
			TotalTokens:            15,
			LlmChatCompletionCount: 1,
		},
	}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-usage-breakdown",
		RunID:           "run-usage-2",
		InitialMessage:  "second",
		AssistantText:   "second answer",
		UpdatedAtMillis: 2000,
		Usage: chat.UsageData{
			PromptTokens:           7,
			CompletionTokens:       3,
			TotalTokens:            10,
			ReasoningTokens:        2,
			LlmChatCompletionCount: 1,
		},
	}); err != nil {
		t.Fatalf("complete second run: %v", err)
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-usage-breakdown", false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.Usage == nil || detail.Usage.LastRun == nil || detail.Usage.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", detail.Usage)
	}
	if detail.Usage.LastRun.PromptTokens != 7 || detail.Usage.LastRun.CompletionTokens != 3 ||
		detail.Usage.LastRun.TotalTokens != 10 || detail.Usage.LastRun.LlmChatCompletionCount != 1 {
		t.Fatalf("expected last run usage, got %#v", detail.Usage.LastRun)
	}
	if detail.Usage.LastRun.CompletionTokensDetails == nil || detail.Usage.LastRun.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("expected last run detail usage, got %#v", detail.Usage.LastRun)
	}
	if detail.Usage.Chat.PromptTokens != 17 || detail.Usage.Chat.CompletionTokens != 8 ||
		detail.Usage.Chat.TotalTokens != 25 || detail.Usage.Chat.LlmChatCompletionCount != 2 {
		t.Fatalf("expected chat cumulative usage, got %#v", detail.Usage.Chat)
	}
	if len(detail.Runs) != 2 || detail.Runs[0].RunID != "run-usage-2" || detail.Runs[0].Usage.TotalTokens != 10 {
		t.Fatalf("expected latest run first, got %#v", detail.Runs)
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

func TestLoadChatDetailIncludesActiveRunAndConflictReturnsHTTP409(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	runs := contracts.NewInMemoryRunManager()
	server.deps.Runs = runs

	if _, _, err := chats.EnsureChat("chat-live", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-live", chat.QueryLine{
		ChatID:    "chat-live",
		RunID:     "run-done",
		UpdatedAt: 1001,
		Query: map[string]any{
			"chatId":  "chat-live",
			"message": "completed",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append completed query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-live", chat.StepLine{
		ChatID:    "chat-live",
		RunID:     "run-done",
		UpdatedAt: 1002,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "done"}}},
		},
	}); err != nil {
		t.Fatalf("append completed step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-live",
		RunID:           "run-done",
		AssistantText:   "done",
		InitialMessage:  "completed",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("complete run-done: %v", err)
	}
	if err := chats.AppendQueryLine("chat-live", chat.QueryLine{
		ChatID:    "chat-live",
		RunID:     "run-live",
		UpdatedAt: 1003,
		Query: map[string]any{
			"chatId":  "chat-live",
			"message": "still running",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append live query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-live", chat.StepLine{
		ChatID:    "chat-live",
		RunID:     "run-live",
		UpdatedAt: 1004,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "partial"}}},
		},
	}); err != nil {
		t.Fatalf("append live step line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live",
		ChatID:   "chat-live",
		AgentKey: "agent-1",
	})

	detail, err := server.loadChatDetail(context.Background(), "chat-live", false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.RunID != "run-live" {
		t.Fatalf("expected active run in chat detail, got %#v", detail.ActiveRun)
	}
	runCompleteCounts := map[string]int{}
	for _, event := range detail.Events {
		if event.Type != "run.complete" {
			continue
		}
		runCompleteCounts[event.String("runId")]++
	}
	if runCompleteCounts["run-live"] != 0 {
		t.Fatalf("expected active run.complete to be removed, got %#v", detail.Events)
	}
	if runCompleteCounts["run-done"] != 1 {
		t.Fatalf("expected completed run.complete to remain, got %#v", detail.Events)
	}

	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live-2",
		ChatID:   "chat-live",
		AgentKey: "agent-1",
	})

	rec := httptest.NewRecorder()
	server.handleChat(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=chat-live", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Msg != "active_run_conflict" {
		t.Fatalf("expected active_run_conflict, got %#v", resp)
	}
}

func TestBroadcastDefinitionsStayAlignedAcrossHTTPAndWS(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	handlerQuery := mustReadFile(t, filepath.Join(root, "handler_query.go"))
	handlerQueryPrepare := mustReadFile(t, filepath.Join(root, "handler_query_prepare.go"))
	handlerChat := mustReadFile(t, filepath.Join(root, "handler_chat.go"))
	wsRoutes := mustReadFile(t, filepath.Join(root, "ws_routes.go"))
	wsQueryRoutes := mustReadFile(t, filepath.Join(root, "ws_query_routes.go"))

	assertContains(t, handlerQuery, `s.broadcast("run.started"`)
	assertContains(t, handlerQuery, `s.broadcast("run.finished"`)
	assertContains(t, handlerQueryPrepare, `s.broadcast("chat.created"`)
	assertContains(t, handlerChat, `s.broadcastChatReadState("chat.read"`)
	assertContains(t, handlerQuery, `s.broadcastChatReadState("chat.unread"`)
	assertContains(t, wsRoutes, `handler.RegisterRoute("/api/attach"`)
	assertContains(t, wsQueryRoutes, `s.broadcast("run.started"`)
	assertContains(t, wsQueryRoutes, `s.broadcast("run.finished"`)
	assertContains(t, wsRoutes, `s.broadcastChatReadState("chat.read"`)
	assertContains(t, wsQueryRoutes, `s.broadcastChatReadState("chat.unread"`)
}

func TestGatewayPullPathAndURLBuilderUsePullEndpoint(t *testing.T) {
	if config.GatewayDownloadPath != "/api/pull" {
		t.Fatalf("expected GatewayDownloadPath /api/pull, got %q", config.GatewayDownloadPath)
	}
	server, _, _ := newServerForHelperTests(t)
	if got := server.buildGatewayURL("https://gateway.example", "ticket-1"); got != "https://gateway.example/api/pull/ticket-1" {
		t.Fatalf("unexpected gateway pull url: %q", got)
	}
}

func TestListAgentSummariesIncludesChatStats(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	server.deps.Registry = wsRegressionCatalogRegistry{
		items: []api.AgentSummary{
			{Key: "agent-a", Name: "Agent A"},
			{Key: "agent-b", Name: "Agent B"},
		},
	}

	if _, _, err := chats.EnsureChat("chat-a1", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a1: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-a2", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a2: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-b1", "agent-b", "", "hello"); err != nil {
		t.Fatalf("ensure chat-b1: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-a2", RunID: "loyw3v20", UpdatedAtMillis: 1000}); err != nil {
		t.Fatalf("complete chat-a2: %v", err)
	}
	if _, err := chats.MarkRead("chat-a2", "loyw3v20"); err != nil {
		t.Fatalf("mark chat-a2 read: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-a1", RunID: "loyw3v28", UpdatedAtMillis: 3000}); err != nil {
		t.Fatalf("complete chat-a1: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-b1", RunID: "loyw3v2s", UpdatedAtMillis: 2000}); err != nil {
		t.Fatalf("complete chat-b1: %v", err)
	}
	if _, err := chats.MarkRead("chat-b1", "loyw3v2s"); err != nil {
		t.Fatalf("mark chat-b1 read: %v", err)
	}

	items, err := server.listAgentSummaries(0, "")
	if err != nil {
		t.Fatalf("list agent summaries: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two agent summaries, got %#v", items)
	}
	statsByKey := map[string]api.AgentChatStats{}
	for _, item := range items {
		statsByKey[item.Key] = item.Stats
	}
	if got := statsByKey["agent-a"]; got.TotalCount != 2 || got.UnreadCount != 1 {
		t.Fatalf("unexpected agent-a stats: %#v", got)
	}
	if got := statsByKey["agent-b"]; got.TotalCount != 1 || got.UnreadCount != 0 {
		t.Fatalf("unexpected agent-b stats: %#v", got)
	}

	items, err = server.listAgentSummaries(1, "")
	if err != nil {
		t.Fatalf("list agent summaries with chats: %v", err)
	}
	chatsByKey := map[string][]api.ChatSummaryResponse{}
	for _, item := range items {
		chatsByKey[item.Key] = item.Chats
	}
	if got := chatsByKey["agent-a"]; len(got) != 1 || got[0].ChatID != "chat-a1" {
		t.Fatalf("unexpected agent-a chats: %#v", got)
	}
	if got := chatsByKey["agent-b"]; len(got) != 1 || got[0].ChatID != "chat-b1" {
		t.Fatalf("unexpected agent-b chats: %#v", got)
	}
}

type wsRegressionCatalogRegistry struct {
	items []api.AgentSummary
}

func (r wsRegressionCatalogRegistry) Agents(string) []api.AgentSummary {
	return append([]api.AgentSummary(nil), r.items...)
}

func (wsRegressionCatalogRegistry) Teams() []api.TeamSummary { return nil }

func (wsRegressionCatalogRegistry) Skills(string) []api.SkillSummary { return nil }

func (wsRegressionCatalogRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}

func (wsRegressionCatalogRegistry) Tools(string, string) []api.ToolSummary { return nil }

func (wsRegressionCatalogRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}

func (wsRegressionCatalogRegistry) DefaultAgentKey() string { return "" }

func (wsRegressionCatalogRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if strings.TrimSpace(key) == "" {
		return catalog.AgentDefinition{}, false
	}
	return catalog.AgentDefinition{
		Key:           key,
		Name:          key,
		ModelKey:      "mock-model",
		MemoryEnabled: true,
	}, true
}

func (wsRegressionCatalogRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}

func (wsRegressionCatalogRegistry) Reload(context.Context, string) error { return nil }

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
			Config: config.Config{
				Memory: config.MemoryConfig{
					Enabled: true,
				},
			},
			Chats:    chats,
			Memory:   memories,
			Registry: wsRegressionCatalogRegistry{},
		},
		ticketService: NewResourceTicketService(config.ResourceTicketConfig{}),
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
