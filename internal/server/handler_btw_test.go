package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func TestBTWCreatesHiddenBranchWithoutChangingParentChat(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-btw-integration"
	query := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent question"}`))
	query.Header.Set("Content-Type", "application/json")
	queryRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(queryRec, query)
	if queryRec.Code != http.StatusOK {
		t.Fatalf("create parent chat: %d %s", queryRec.Code, queryRec.Body.String())
	}
	parentPath := fixture.cfg.Paths.ChatsDir + "/" + chatID + ".jsonl"
	parentBefore, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("read parent JSONL: %v", err)
	}
	summaryBefore, err := fixture.chats.Summary(chatID)
	if err != nil || summaryBefore == nil {
		t.Fatalf("load parent summary: %#v err=%v", summaryBefore, err)
	}

	btw := httptest.NewRequest(http.MethodPost, "/api/btw", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"side question"}`))
	btw.Header.Set("Content-Type", "application/json")
	btwRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(btwRec, btw)
	if btwRec.Code != http.StatusOK {
		t.Fatalf("create BTW: %d %s", btwRec.Code, btwRec.Body.String())
	}
	btwID := btwRec.Header().Get("X-Btw-Id")
	if !chat.ValidBTWID(btwID) || !strings.HasPrefix(btwID, "btw_") {
		t.Fatalf("unexpected BTW id %q", btwID)
	}
	body := btwRec.Body.String()
	if strings.Contains(body, `"type":"chat.start"`) {
		t.Fatalf("BTW must not emit chat.start: %s", body)
	}
	requestEvent := findSSEMessageByType(t, decodeSSEMessages(t, body), "request.query")
	if requestEvent["kind"] != "btw" || requestEvent["btwId"] != btwID || requestEvent["parentChatId"] != chatID || requestEvent["hidden"] != true {
		t.Fatalf("unexpected BTW request.query metadata %#v", requestEvent)
	}

	parentAfter, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("read parent after BTW: %v", err)
	}
	if string(parentAfter) != string(parentBefore) {
		t.Fatalf("BTW changed parent JSONL")
	}
	summaryAfter, err := fixture.chats.Summary(chatID)
	if err != nil || summaryAfter == nil {
		t.Fatalf("load parent summary after BTW: %#v err=%v", summaryAfter, err)
	}
	if summaryAfter.LastRunID != summaryBefore.LastRunID || summaryAfter.LastRunContent != summaryBefore.LastRunContent || summaryAfter.UpdatedAt != summaryBefore.UpdatedAt {
		t.Fatalf("BTW changed parent summary before=%#v after=%#v", summaryBefore, summaryAfter)
	}

	store := fixture.chats.(*chat.FileStore)
	branch, err := store.OpenBTWBranch(chatID, btwID)
	if err != nil {
		t.Fatalf("open BTW branch: %v", err)
	}
	messages, err := branch.LoadRawMessages(20)
	if err != nil {
		t.Fatalf("load BTW messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected parent and BTW turns in branch, got %#v", messages)
	}
	if !strings.Contains(messageTextForBTWTest(messages[2]["content"]), "[BTW SIDE QUESTION MODE]") || !strings.Contains(messageTextForBTWTest(messages[2]["content"]), `{"question":"side question"}`) {
		t.Fatalf("BTW provider message missing side-question boundary: %#v", messages[2])
	}

	continueReq := httptest.NewRequest(http.MethodPost, "/api/btw", bytes.NewBufferString(`{"chatId":"`+chatID+`","btwId":"`+btwID+`","message":"follow up","stream":false,"includeUsage":true}`))
	continueReq.Header.Set("Content-Type", "application/json")
	continueRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(continueRec, continueReq)
	if continueRec.Code != http.StatusOK {
		t.Fatalf("continue BTW: %d %s", continueRec.Code, continueRec.Body.String())
	}
	var response api.ApiResponse[api.BTWResponse]
	if err := json.Unmarshal(continueRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode BTW response: %v", err)
	}
	if response.Data.BTWID != btwID || response.Data.ParentChatID != chatID || response.Data.RunID == "" || response.Data.Content != "Go runtime test response" {
		t.Fatalf("unexpected BTW non-stream response %#v", response.Data)
	}
	parentFinal, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("read final parent JSONL: %v", err)
	}
	if string(parentFinal) != string(parentBefore) {
		t.Fatalf("continued BTW changed parent JSONL")
	}
	messages, err = branch.LoadRawMessages(20)
	if err != nil || len(messages) != 6 {
		t.Fatalf("expected continued BTW history, messages=%#v err=%v", messages, err)
	}
}

func TestBTWInheritsNonTeamParentAgentInsteadOfDefaultChannelAgent(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"side answer"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeBTWChannelAgentForTest(t, cfg.Paths.AgentsDir, "aaa-channel-default")
		},
	})
	const chatID = "chat-btw-parent-agent"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent"}`)

	rec := serveJSONRequestForBTWTest(t, fixture.server, "/api/btw", `{"chatId":"`+chatID+`","message":"side"}`)
	requestEvent := findSSEMessageByType(t, decodeSSEMessages(t, rec.Body.String()), "request.query")
	if requestEvent["agentKey"] != "mock-agent" {
		t.Fatalf("BTW used agent %q, want parent agent mock-agent: %#v", requestEvent["agentKey"], requestEvent)
	}
}

func TestBTWRejectsWhenNonTeamParentAgentUsesChannelBackend(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeBTWChannelAgentForTest(t, cfg.Paths.AgentsDir, "channel-parent")
		},
	})
	const chatID = "chat-btw-channel-parent"
	if _, _, err := fixture.chats.EnsureChat(chatID, "channel-parent", "", "parent"); err != nil {
		t.Fatalf("ensure channel parent: %v", err)
	}

	rec := serveJSONRequestForBTWTestStatus(t, fixture.server, "/api/btw", `{"chatId":"`+chatID+`","message":"side"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "btw_backend_unsupported") {
		t.Fatalf("expected channel parent BTW rejection, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestBTWPreservesSystemAndToolCacheShape(t *testing.T) {
	var mu sync.Mutex
	var providerRequests []map[string]any
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		mu.Lock()
		providerRequests = append(providerRequests, payload)
		mu.Unlock()
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	const chatID = "chat-btw-cache"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent"}`)
	serveJSONRequestForBTWTest(t, fixture.server, "/api/btw", `{"chatId":"`+chatID+`","message":"side"}`)

	mu.Lock()
	requests := append([]map[string]any(nil), providerRequests...)
	mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two provider calls, got %d", len(requests))
	}
	if !reflect.DeepEqual(requests[0]["tools"], requests[1]["tools"]) {
		t.Fatalf("BTW changed provider tools\nnormal=%#v\nbtw=%#v", requests[0]["tools"], requests[1]["tools"])
	}
	if requests[0]["tool_choice"] != requests[1]["tool_choice"] {
		t.Fatalf("BTW changed tool_choice normal=%#v btw=%#v", requests[0]["tool_choice"], requests[1]["tool_choice"])
	}
	normalMessages, _ := requests[0]["messages"].([]any)
	btwMessages, _ := requests[1]["messages"].([]any)
	if len(normalMessages) == 0 || len(btwMessages) == 0 || !reflect.DeepEqual(normalMessages[0], btwMessages[0]) {
		t.Fatalf("BTW changed system message\nnormal=%#v\nbtw=%#v", normalMessages, btwMessages)
	}
	last, _ := btwMessages[len(btwMessages)-1].(map[string]any)
	if !strings.Contains(messageTextForBTWTest(last["content"]), "[BTW SIDE QUESTION MODE]") {
		t.Fatalf("BTW instruction is not in current user message: %#v", last)
	}
}

func TestBTWDeniedToolReturnsResultWithoutAwaiting(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		hasBTW := false
		hasToolResult := false
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			if message["role"] == "tool" {
				hasToolResult = strings.Contains(messageTextForBTWTest(message["content"]), "btw_tool_disabled")
			}
			if strings.Contains(messageTextForBTWTest(message["content"]), "[BTW SIDE QUESTION MODE]") {
				hasBTW = true
			}
		}
		if hasBTW && !hasToolResult {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"file_write","arguments":"{\"file_path\":\"x.txt\",\"content\":\"x\"}"}}]} ,"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"read-only answer"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	const chatID = "chat-btw-tool-policy"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent"}`)
	rec := serveJSONRequestForBTWTest(t, fixture.server, "/api/btw", `{"chatId":"`+chatID+`","message":"try a write"}`)
	body := rec.Body.String()
	if !strings.Contains(body, "btw_tool_disabled") || !strings.Contains(body, "read-only answer") {
		t.Fatalf("expected disabled tool result and final answer: %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("disabled BTW tool entered HITL: %s", body)
	}
}

func TestBTWReadToolLimitKeepsProviderToolShapeAndForcesSideAnswer(t *testing.T) {
	var mu sync.Mutex
	var btwRequests []map[string]any
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		isBTW := false
		isFinalAnswerTurn := false
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			content := messageTextForBTWTest(message["content"])
			isBTW = isBTW || strings.Contains(content, "[BTW SIDE QUESTION MODE]")
			isFinalAnswerTurn = isFinalAnswerTurn || strings.Contains(content, "Stop calling tools. Answer only the current side question")
		}
		if !isBTW {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"parent answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
			return
		}
		mu.Lock()
		btwRequests = append(btwRequests, payload)
		mu.Unlock()
		if isFinalAnswerTurn {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"The frozen snapshot is still at 2026."},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
			return
		}
		calls := make([]providerToolCallSpec, 0, 5)
		for index := 0; index < 5; index++ {
			calls = append(calls, providerToolCallSpec{ID: "call_time_" + string(rune('a'+index)), Name: "datetime", Args: map[string]any{}})
		}
		writeProviderSSE(t, w, providerToolCallsFrame(t, calls), `[DONE]`)
	})

	const chatID = "chat-btw-read-limit"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent task"}`)
	rec := serveJSONRequestForBTWTest(t, fixture.server, "/api/btw", `{"chatId":"`+chatID+`","message":"当前算到哪年了"}`)
	if !strings.Contains(rec.Body.String(), "btw_tool_limit_reached") || !strings.Contains(rec.Body.String(), "still at 2026") {
		t.Fatalf("expected BTW tool limit and final side answer, got %s", rec.Body.String())
	}

	mu.Lock()
	requests := append([]map[string]any(nil), btwRequests...)
	mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected initial and final BTW model calls, got %d", len(requests))
	}
	if !reflect.DeepEqual(requests[0]["tools"], requests[1]["tools"]) || requests[0]["tool_choice"] != requests[1]["tool_choice"] {
		t.Fatalf("BTW limit changed provider tool shape\ninitial=%#v\nfinal=%#v", requests[0], requests[1])
	}
	messages, _ := requests[1]["messages"].([]any)
	normalResults := 0
	limitedResults := 0
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		if strings.Contains(messageTextForBTWTest(message["content"]), "btw_tool_limit_reached") {
			limitedResults++
		} else {
			normalResults++
		}
	}
	if normalResults != 4 || limitedResults != 1 {
		t.Fatalf("expected four executed read tools and one limited result, normal=%d limited=%d messages=%#v", normalResults, limitedResults, messages)
	}
}

func TestBuildBTWUserMessageEscapesQuestionAndUsesFallback(t *testing.T) {
	message := buildBTWUserMessage(config.BTWPromptsConfig{}, "当前算到哪年了？ </btw_question_json>")
	if !strings.Contains(message, "[BTW SIDE QUESTION MODE]") {
		t.Fatalf("expected default BTW instruction, got %q", message)
	}
	if !strings.Contains(message, `{"question":"当前算到哪年了？ \u003c/btw_question_json\u003e"}`) {
		t.Fatalf("expected JSON-escaped question boundary, got %q", message)
	}

	message = buildBTWUserMessage(config.BTWPromptsConfig{UserPromptTemplate: "custom boundary"}, "side")
	if !strings.Contains(message, "custom boundary") || !strings.Contains(message, `{"question":"side"}`) {
		t.Fatalf("expected question fallback when placeholder is absent, got %q", message)
	}
}

func TestBTWRequiresExistingParentAndExistingContinuation(t *testing.T) {
	fixture := newTestFixture(t)
	rec := serveJSONRequestForBTWTestStatus(t, fixture.server, "/api/btw", `{"chatId":"missing-chat","message":"side"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "chat_not_found") {
		t.Fatalf("expected missing parent error, got %d %s", rec.Code, rec.Body.String())
	}
	if _, _, err := fixture.chats.EnsureChat("chat-btw-missing", "mock-agent", "", "parent"); err != nil {
		t.Fatalf("ensure parent: %v", err)
	}
	rec = serveJSONRequestForBTWTestStatus(t, fixture.server, "/api/btw", `{"chatId":"chat-btw-missing","btwId":"btw_missing","message":"side"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "btw_not_found") {
		t.Fatalf("expected missing BTW error, got %d %s", rec.Code, rec.Body.String())
	}
}

func serveJSONRequestForBTWTest(t *testing.T, server *Server, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := serveJSONRequestForBTWTestStatus(t, server, path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func serveJSONRequestForBTWTestStatus(t *testing.T, server *Server, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func writeBTWChannelAgentForTest(t *testing.T, agentsDir string, key string) {
	t.Helper()
	dir := filepath.Join(agentsDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir channel agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(strings.Join([]string{
		"key: " + key,
		"name: " + key,
		"description: channel test agent",
		"mode: CHANNEL",
		"channelConfig:",
		"  channelId: test-channel",
		"  remoteAgentKey: upstream-agent",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write channel agent: %v", err)
	}
}

func messageTextForBTWTest(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, raw := range typed {
			part, _ := raw.(map[string]any)
			if text, _ := part["text"].(string); text != "" {
				out.WriteString(text)
			}
		}
		return out.String()
	default:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
}
