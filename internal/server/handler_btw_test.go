package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
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
	if !strings.Contains(messageTextForBTWTest(messages[2]["content"]), btwReadOnlyUserInstruction) || !strings.Contains(messageTextForBTWTest(messages[2]["content"]), "side question") {
		t.Fatalf("BTW provider message missing read-only instruction: %#v", messages[2])
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
	if !strings.Contains(messageTextForBTWTest(last["content"]), btwReadOnlyUserInstruction) {
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
			if strings.Contains(messageTextForBTWTest(message["content"]), btwReadOnlyUserInstruction) {
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
