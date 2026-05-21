package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func TestQuerySSEPersistsChatHistory(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	body := bytes.NewBufferString(`{"message":"元素碳的简介，100字","agentKey":"mock-agent"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("expected sse content type, got %q", got)
	}
	bodyText := rec.Body.String()
	if !strings.Contains(bodyText, `"type":"request.query"`) {
		t.Fatalf("expected request.query event, got %s", bodyText)
	}
	if strings.Contains(bodyText, `.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", bodyText)
	}
	if !strings.Contains(bodyText, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", bodyText)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, bodyText)
	assertSSEEventOrder(t, bodyText, "request.query", "chat.start", "run.start")

	chatsReq := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, chatsReq)

	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}
	chatID := chatsResp.Data[0].ChatID
	assertUUIDLike(t, chatID)

	chatReq := httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil)
	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, chatReq)

	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if len(chatResp.Data.Events) < 4 {
		t.Fatalf("expected persisted events, got %#v", chatResp.Data.Events)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"content.snapshot",
		"run.complete",
	)
	assertBodyContainsOrderedEvent(t, chatRec.Body.String(), `"type":"request.query"`, []string{
		`"seq":`,
		`"type":"request.query"`,
		`"requestId":`,
		`"runId":`,
		`"chatId":`,
		`"timestamp":`,
	})
	if len(chatResp.Data.RawMessages) != 2 {
		t.Fatalf("expected 2 raw messages, got %#v", chatResp.Data.RawMessages)
	}
}

func TestQueryUsesProvidedRunIDAndPersistsItEverywhere(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server
	runID := "loyw3v28"

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"reuse run id","runId":"`+runID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	messages := decodeSSEMessages(t, body)
	if len(messages) < 3 {
		t.Fatalf("expected bootstrap messages, got %#v", messages)
	}
	if messages[0]["type"] != "request.query" || messages[0]["runId"] != runID {
		t.Fatalf("expected request.query to carry provided run id, got %#v", messages[0])
	}
	if messages[2]["type"] != "run.start" || messages[2]["runId"] != runID {
		t.Fatalf("expected run.start to carry provided run id, got %#v", messages[2])
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 || chatsResp.Data[0].LastRunID != runID {
		t.Fatalf("expected summary lastRunId=%s, got %#v", runID, chatsResp.Data)
	}

	chatReq := httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID+"&includeRawMessages=true", nil)
	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, chatReq)
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	foundRequest := false
	for _, event := range chatResp.Data.Events {
		if event.Type != "request.query" {
			continue
		}
		foundRequest = true
		if got := event.String("runId"); got != runID {
			t.Fatalf("expected persisted request.query run id, got %#v", event)
		}
	}
	if !foundRequest {
		t.Fatalf("expected persisted request.query event, got %#v", chatResp.Data.Events)
	}
	for _, message := range chatResp.Data.RawMessages {
		if got := message["runId"]; got != runID {
			t.Fatalf("expected raw message runId=%s, got %#v", runID, message)
		}
	}
}

func TestQueryRequestQueryIncludesParamsAndReferences(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	body := bytes.NewBufferString(`{
		"message":"include request context",
		"params":{"channel":"desktop","nested":{"enabled":true}},
		"references":[{"id":"ref_1","type":"file","name":"notes.txt","mimeType":"text/plain","url":"file:///tmp/notes.txt"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	messages := decodeSSEMessages(t, rec.Body.String())
	if len(messages) < 1 || messages[0]["type"] != "request.query" {
		t.Fatalf("expected first sse message to be request.query, got %#v", messages)
	}
	assertRequestQueryContext(t, messages[0])

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %#v", chatsResp.Data)
	}

	chatReq := httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID+"&includeRawMessages=true", nil)
	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, chatReq)
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	foundRequest := false
	for _, event := range chatResp.Data.Events {
		if event.Type != "request.query" {
			continue
		}
		foundRequest = true
		assertRequestQueryContext(t, event.Map())
	}
	if !foundRequest {
		t.Fatalf("expected persisted request.query event, got %#v", chatResp.Data.Events)
	}
}

func assertRequestQueryContext(t *testing.T, message map[string]any) {
	t.Helper()
	params, ok := message["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected request.query params object, got %#v", message)
	}
	if params["channel"] != "desktop" {
		t.Fatalf("expected request.query params.channel desktop, got %#v", params)
	}
	nested, ok := params["nested"].(map[string]any)
	if !ok || nested["enabled"] != true {
		t.Fatalf("expected request.query params.nested.enabled true, got %#v", params)
	}
	references, ok := message["references"].([]any)
	if !ok || len(references) != 1 {
		t.Fatalf("expected request.query single reference, got %#v", message)
	}
	reference, ok := references[0].(map[string]any)
	if !ok {
		t.Fatalf("expected request.query reference object, got %#v", references[0])
	}
	if reference["id"] != "ref_1" || reference["type"] != "file" || reference["name"] != "notes.txt" {
		t.Fatalf("unexpected request.query reference, got %#v", reference)
	}
}

func TestQueryGeneratesBase36RunIDWhenMissing(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"generate run id"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	messages := decodeSSEMessages(t, rec.Body.String())
	if len(messages) < 3 {
		t.Fatalf("expected bootstrap messages, got %#v", messages)
	}
	runID, _ := messages[2]["runId"].(string)
	if runID == "" || strings.HasPrefix(runID, "run_") {
		t.Fatalf("expected new base36 run id, got %q", runID)
	}
	if millis, ok := chat.ParseRunIDMillis(runID); !ok || millis <= 0 {
		t.Fatalf("expected generated run id to parse as epoch millis, got %q millis=%d ok=%v", runID, millis, ok)
	}
}

func TestRememberEndpointReturnsStoredMemory(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	queryReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"记住这个答案"}`))
	queryReq.Header.Set("Content-Type", "application/json")
	queryRec := httptest.NewRecorder()
	server.ServeHTTP(queryRec, queryReq)

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))

	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	chatID := chatsResp.Data[0].ChatID

	rememberReq := httptest.NewRequest(http.MethodPost, "/api/remember", bytes.NewBufferString(`{"requestId":"req_remember","chatId":"`+chatID+`"}`))
	rememberReq.Header.Set("Content-Type", "application/json")
	rememberRec := httptest.NewRecorder()
	server.ServeHTTP(rememberRec, rememberReq)

	if rememberRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rememberRec.Code, rememberRec.Body.String())
	}
	var rememberResp api.ApiResponse[api.RememberResponse]
	if err := json.Unmarshal(rememberRec.Body.Bytes(), &rememberResp); err != nil {
		t.Fatalf("decode remember response: %v", err)
	}
	if !rememberResp.Data.Accepted {
		t.Fatalf("expected remember accepted, got %#v", rememberResp.Data)
	}
	if rememberResp.Data.MemoryCount != 1 {
		t.Fatalf("expected one memory item, got %#v", rememberResp.Data)
	}
	if !strings.HasPrefix(rememberResp.Data.MemoryPath, fixture.cfg.Paths.MemoryDir+string(os.PathSeparator)) {
		t.Fatalf("expected memory path under %s, got %s", fixture.cfg.Paths.MemoryDir, rememberResp.Data.MemoryPath)
	}
}

func TestChatSnapshotDeduplicatesChatStartAcrossMultipleQueries(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	firstReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"first turn"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first query expected 200, got %d: %s", firstRec.Code, firstRec.Body.String())
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat after first query, got %#v", chatsResp.Data)
	}
	chatID := chatsResp.Data[0].ChatID

	secondReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"second turn"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second query expected 200, got %d: %s", secondRec.Code, secondRec.Body.String())
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
	if chatRec.Code != http.StatusOK {
		t.Fatalf("chat detail expected 200, got %d: %s", chatRec.Code, chatRec.Body.String())
	}

	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}

	chatStartCount := 0
	runStartCount := 0
	prevSeq := int64(0)
	for _, event := range chatResp.Data.Events {
		eventType := event.Type
		switch eventType {
		case "chat.start":
			chatStartCount++
		case "run.start":
			runStartCount++
		}
		seq := event.Seq
		if seq != prevSeq+1 {
			t.Fatalf("expected contiguous seq values, got prev=%d current=%d events=%#v", prevSeq, seq, chatResp.Data.Events)
		}
		prevSeq = seq
	}
	if chatStartCount != 1 {
		t.Fatalf("expected one chat.start in snapshot, got %d events=%#v", chatStartCount, chatResp.Data.Events)
	}
	if runStartCount != 2 {
		t.Fatalf("expected two run.start events, got %d events=%#v", runStartCount, chatResp.Data.Events)
	}
	if len(chatResp.Data.Events) != 9 {
		t.Fatalf("expected 9 persisted events for two turns, got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
	}
	if len(chatResp.Data.RawMessages) != 4 {
		t.Fatalf("expected four raw messages for two turns, got %#v", chatResp.Data.RawMessages)
	}
}

func TestQueryCanExecuteBackendToolLoop(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		hasToolMessage := false
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if role, _ := message["role"].(string); role == "tool" {
				hasToolMessage = true
				break
			}
		}
		if !hasToolMessage {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"datetime","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"完成工具调用后"}}]}`,
			`{"choices":[{"delta":{"content":"的最终回答"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	server := fixture.server

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"tool.start"`) {
		t.Fatalf("expected tool.start event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.args"`) {
		t.Fatalf("expected tool.args event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool.end event, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.snapshot"`) || strings.Contains(body, `"type":"content.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if strings.Contains(body, `"toolType":`) {
		t.Fatalf("did not expect toolType in live sse, got %s", body)
	}
	if strings.Contains(body, `"viewportKey":`) {
		t.Fatalf("did not expect viewportKey for backend tool, got %s", body)
	}
	if !strings.Contains(body, "完成工具调用后") || !strings.Contains(body, "的最终回答") {
		t.Fatalf("expected live sse deltas for final assistant content, got %s", body)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, body)
	assertSSEPayloadOrder(t, body, "tool.start", []string{
		`"seq":`,
		`"type":"tool.start"`,
		`"toolId":"`,
		`"runId":"`,
		`"timestamp":`,
	})

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"content.snapshot",
		"run.complete",
	)
}

func TestQueryDecryptsAESProviderAPIKeyBeforeSendingAuthorizationHeader(t *testing.T) {
	const envPart = "server-test-env-secret"
	const plainAPIKey = "test-key"

	t.Setenv("PROVIDER_APIKEY_KEY_PART", envPart)

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+plainAPIKey {
			t.Fatalf("expected decrypted Authorization header, got %q", got)
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		setupRuntime: func(root string, _ *config.Config) {
			providerConfig := strings.Join([]string{
				"key: mock",
				"baseUrl: http://placeholder.invalid",
				"apiKey: " + mustEncryptProviderAPIKeyForServerTest(t, envPart, plainAPIKey),
				"defaultModel: mock-model",
			}, "\n")
			providerPath := filepath.Join(root, "registries", "providers", "mock.yml")
			if err := os.WriteFile(providerPath, []byte(providerConfig), 0o644); err != nil {
				t.Fatalf("write encrypted provider config: %v", err)
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hello","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", rec.Body.String())
	}
}

func TestQueryAndRunDebugEventsDisabledByDefault(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hide debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertStringSliceExcludes(t, decodeEventTypesFromSSE(t, body), "debug.preCall", "debug.postCall")

	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got %s", body)
	}
	runID, _ := messages[0]["runId"].(string)
	chatID, _ := messages[0]["chatId"].(string)
	if runID == "" || chatID == "" {
		t.Fatalf("expected runId/chatId in first sse message, got %#v", messages[0])
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID, nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	assertStringSliceExcludes(t, decodeEventTypesFromSSE(t, runRec.Body.String()), "debug.preCall", "debug.postCall")

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertEventTypesExclude(t, chatResp.Data.Events, "debug.preCall", "debug.postCall")
}

func TestQueryAndRunDebugEventsEnabledWhenEnabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Stream.DebugEventsEnabled = true
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"show debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertStringSliceContains(t, decodeEventTypesFromSSE(t, body), "debug.preCall", "debug.postCall")

	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got %s", body)
	}
	var preCall map[string]any
	for _, message := range messages {
		if eventType, _ := message["type"].(string); eventType == "debug.preCall" {
			preCall = message
			break
		}
	}
	if preCall == nil {
		t.Fatalf("expected debug.preCall in sse stream, got %#v", messages)
	}
	preCallData, _ := preCall["data"].(map[string]any)
	provider, _ := preCallData["provider"].(map[string]any)
	model, _ := preCallData["model"].(map[string]any)
	requestBody, _ := preCallData["requestBody"].(map[string]any)
	injectedPrompt, _ := preCallData["injectedPrompt"].(map[string]any)
	if provider["key"] != "mock" {
		t.Fatalf("expected provider key mock, got %#v", provider)
	}
	if !strings.HasSuffix(stringValue(provider["endpoint"]), "/v1/chat/completions") {
		t.Fatalf("unexpected provider endpoint %#v", provider)
	}
	if model["key"] != "mock-model" || model["id"] != "mock-model-id" {
		t.Fatalf("unexpected model payload %#v", model)
	}
	if len(requestBody) == 0 {
		t.Fatalf("expected requestBody payload, got %#v", preCallData)
	}
	if len(injectedPrompt) == 0 {
		t.Fatalf("expected injectedPrompt payload, got %#v", preCallData)
	}
	if got := strings.TrimSpace(stringValue(injectedPrompt["systemPrompt"])); got == "" {
		t.Fatalf("expected injectedPrompt.systemPrompt, got %#v", injectedPrompt)
	}
	providerMessages, _ := injectedPrompt["providerMessages"].([]any)
	if len(providerMessages) == 0 {
		t.Fatalf("expected injectedPrompt.providerMessages, got %#v", injectedPrompt)
	}
	if _, exists := preCallData["systemPrompt"]; exists {
		t.Fatalf("did not expect systemPrompt in debug.preCall payload, got %#v", preCallData)
	}
	if _, exists := preCallData["tools"]; exists {
		t.Fatalf("did not expect tools in debug.preCall payload, got %#v", preCallData)
	}
	runID, _ := messages[0]["runId"].(string)
	chatID, _ := messages[0]["chatId"].(string)
	if runID == "" || chatID == "" {
		t.Fatalf("expected runId/chatId in first sse message, got %#v", messages[0])
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID, nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	assertStringSliceContains(t, decodeEventTypesFromSSE(t, runRec.Body.String()), "debug.preCall", "debug.postCall")

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertEventTypesInclude(t, chatResp.Data.Events, "debug.preCall", "debug.postCall")
}

func TestPlanExecutePlanStageOnlyUsesPlanAddTasksBeforeSequentialTaskExecution(t *testing.T) {
	var providerCallCount atomic.Int32

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		toolNames := providerRequestToolNames(payload["tools"])

		switch call := providerCallCount.Add(1); call {
		case 1:
			if !reflect.DeepEqual(toolNames, []string{"plan_add_tasks"}) {
				t.Fatalf("plan stage tools=%#v want only plan_add_tasks", toolNames)
			}
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan", "plan_add_tasks", map[string]any{
					"tasks": []map[string]any{
						{"taskId": "task_alpha", "description": "查询当前时间"},
						{"taskId": "task_beta", "description": "再次查询当前时间"},
					},
				}),
				`[DONE]`,
			)
		case 2:
			assertStringSliceContains(t, toolNames, "datetime", "memory_search", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time_alpha", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 3:
			assertStringSliceContains(t, toolNames, "datetime", "memory_search", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_done_alpha", "plan_update_task", map[string]any{
					"taskId": "task_alpha",
					"status": "completed",
				}),
				`[DONE]`,
			)
		case 4:
			assertStringSliceContains(t, toolNames, "datetime", "memory_search", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time_beta", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 5:
			assertStringSliceContains(t, toolNames, "datetime", "memory_search", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_done_beta", "plan_update_task", map[string]any{
					"taskId": "task_beta",
					"status": "completed",
				}),
				`[DONE]`,
			)
		case 6:
			if len(toolNames) != 0 {
				t.Fatalf("summary stage should not expose tools, got %#v", toolNames)
			}
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"任务已按顺序完成"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Stream.DebugEventsEnabled = true
			cfg.Memory.Enabled = true
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
				"key: mock-agent",
				"name: Mock Agent",
				"role: 测试代理",
				"description: plan execute test agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - datetime",
				"    - memory_search",
				"memoryConfig:",
				"  enabled: true",
				"mode: PLAN_EXECUTE",
				"stageSettings:",
				"  maxWorkRoundsPerTask: 4",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write plan-execute agent config: %v", err)
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"请先规划再按顺序执行两个任务","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := providerCallCount.Load(); got != 6 {
		t.Fatalf("expected 6 provider calls, got %d", got)
	}

	messages := decodeSSEMessages(t, rec.Body.String())
	var preCallTools [][]string
	preTaskToolNames := make([]string, 0)
	firstTaskStartIndex := -1
	taskStarts := map[string]int{}
	taskCompletes := map[string]int{}

	for index, message := range messages {
		switch stringValue(message["type"]) {
		case "debug.preCall":
			data, _ := message["data"].(map[string]any)
			requestBody, _ := data["requestBody"].(map[string]any)
			preCallTools = append(preCallTools, providerRequestToolNames(requestBody["tools"]))
		case "tool.start":
			if firstTaskStartIndex < 0 {
				preTaskToolNames = append(preTaskToolNames, stringValue(message["toolName"]))
			}
		case "task.start":
			if firstTaskStartIndex < 0 {
				firstTaskStartIndex = index
			}
			taskStarts[stringValue(message["taskId"])] = index
		case "task.complete":
			taskCompletes[stringValue(message["taskId"])] = index
		}
	}

	if !reflect.DeepEqual(preTaskToolNames, []string{"plan_add_tasks"}) {
		t.Fatalf("expected only plan_add_tasks before first task.start, got %#v", preTaskToolNames)
	}
	if len(preCallTools) != 6 {
		t.Fatalf("expected 6 debug.preCall events, got %#v", preCallTools)
	}
	if !reflect.DeepEqual(preCallTools[0], []string{"plan_add_tasks"}) {
		t.Fatalf("plan debug.preCall tools=%#v want only plan_add_tasks", preCallTools[0])
	}
	for callIndex := 1; callIndex <= 4; callIndex++ {
		assertStringSliceContains(t, preCallTools[callIndex], "datetime", "memory_search", "plan_update_task")
		assertStringSliceExcludes(t, preCallTools[callIndex], "plan_add_tasks")
	}
	if len(preCallTools[5]) != 0 {
		t.Fatalf("summary debug.preCall tools=%#v want none", preCallTools[5])
	}

	alphaStart, ok := taskStarts["task_alpha"]
	if !ok {
		t.Fatalf("missing task.start for task_alpha in %#v", messages)
	}
	alphaComplete, ok := taskCompletes["task_alpha"]
	if !ok {
		t.Fatalf("missing task.complete for task_alpha in %#v", messages)
	}
	betaStart, ok := taskStarts["task_beta"]
	if !ok {
		t.Fatalf("missing task.start for task_beta in %#v", messages)
	}
	betaComplete, ok := taskCompletes["task_beta"]
	if !ok {
		t.Fatalf("missing task.complete for task_beta in %#v", messages)
	}
	if !(alphaStart < alphaComplete && alphaComplete < betaStart && betaStart < betaComplete) {
		t.Fatalf("expected sequential task execution, got alphaStart=%d alphaComplete=%d betaStart=%d betaComplete=%d", alphaStart, alphaComplete, betaStart, betaComplete)
	}
}

func TestCoderPlanningModeQuestionsConfirmThenExecutes(t *testing.T) {
	var providerCallCount atomic.Int32

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		toolNames := providerRequestToolNames(payload["tools"])
		switch call := providerCallCount.Add(1); call {
		case 1:
			assertCoderPlanningToolSet(t, toolNames)
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_question_one", "ask_user_question", map[string]any{
					"mode": "question",
					"questions": []map[string]any{
						{
							"question": "Which file should I inspect?",
							"type":     "select",
							"options":  []map[string]any{{"label": "README.md"}},
						},
					},
				}),
				`[DONE]`,
			)
		case 2:
			assertCoderPlanningToolSet(t, toolNames)
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_question_two", "ask_user_question", map[string]any{
					"mode": "question",
					"questions": []map[string]any{
						{
							"question": "How broad should the change be?",
							"type":     "select",
							"options":  []map[string]any{{"label": "Small"}},
						},
					},
				}),
				`[DONE]`,
			)
		case 3:
			assertCoderPlanningToolSet(t, toolNames)
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan", "plan_add_tasks", map[string]any{
					"tasks": []map[string]any{
						{"taskId": "task_1", "description": "Check the current time before reporting"},
					},
				}),
				`[DONE]`,
			)
		case 4:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_grep", "datetime", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks", "ask_user_question")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 5:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_grep", "datetime", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks", "ask_user_question")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_done", "plan_update_task", map[string]any{
					"taskId": "task_1",
					"status": "completed",
				}),
				`[DONE]`,
			)
		case 6:
			if len(toolNames) != 0 {
				t.Fatalf("coder planning summary should not expose tools, got %#v", toolNames)
			}
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"confirmed plan completed"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		setupRuntime: func(root string, cfg *config.Config) {
			workspace := filepath.Join(root, "workspace")
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-app")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir coder agent: %v", err)
			}
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: coder-app",
				"name: Coder App",
				"type: CODER",
				"mode: REACT",
				"modelConfig:",
				"  modelKey: mock-model",
				"workspaceConfig:",
				"  root: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please plan first","agentKey":"coder-app","planningMode":true}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID, firstAwaitingID := readAwaitingQuestion(t, reader, &streamBody, "Which file should I inspect?")
	submitFrontendAnswer(t, fixture.server, runID, firstAwaitingID, "README.md")
	_, secondAwaitingID := readAwaitingQuestion(t, reader, &streamBody, "How broad should the change be?")
	submitFrontendAnswer(t, fixture.server, runID, secondAwaitingID, "Small")
	_, confirmAwaitingID := readAwaitingQuestion(t, reader, &streamBody, "是否按此计划执行？")
	if strings.Contains(streamBody.String(), `"type":"task.start"`) {
		t.Fatalf("did not expect task.start before plan confirmation, got %s", streamBody.String())
	}
	if !strings.Contains(streamBody.String(), `"type":"plan.update"`) {
		t.Fatalf("expected plan.update before confirmation, got %s", streamBody.String())
	}
	submitFrontendAnswer(t, fixture.server, runID, confirmAwaitingID, "执行计划")

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after confirmation: %v", readErr)
		}
	}
	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.query"`) || !strings.Contains(body, `"planningMode":true`) {
		t.Fatalf("expected live request.query planningMode=true, got %s", body)
	}
	planIndex := strings.Index(body, `"type":"plan.update"`)
	confirmIndex := strings.Index(body, `是否按此计划执行？`)
	taskStartIndex := strings.Index(body, `"type":"task.start"`)
	if !(planIndex >= 0 && confirmIndex > planIndex && taskStartIndex > confirmIndex) {
		t.Fatalf("expected plan.update before confirmation and task.start after confirmation, got %s", body)
	}
	if !strings.Contains(body, `"answer":"执行计划"`) {
		t.Fatalf("expected confirmation answer in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"task.complete"`) || !strings.Contains(body, "confirmed plan completed") {
		t.Fatalf("expected confirmed execution to complete, got %s", body)
	}
	if got := providerCallCount.Load(); got != 6 {
		t.Fatalf("provider calls = %d, want 6", got)
	}
	assertPersistedPlanningModeRequestQuery(t, fixture.server)
}

func TestCoderPlanningModeCancelDoesNotExecuteTasks(t *testing.T) {
	var providerCallCount atomic.Int32

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		switch call := providerCallCount.Add(1); call {
		case 1:
			assertCoderPlanningToolSet(t, providerRequestToolNames(payload["tools"]))
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan", "plan_add_tasks", map[string]any{
					"tasks": []map[string]any{
						{"taskId": "task_1", "description": "This task must not start"},
					},
				}),
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call after cancel: %d", call)
		}
	}, testFixtureOptions{
		setupRuntime: func(root string, cfg *config.Config) {
			workspace := filepath.Join(root, "workspace")
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-app")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir coder agent: %v", err)
			}
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: coder-app",
				"name: Coder App",
				"type: CODER",
				"mode: REACT",
				"modelConfig:",
				"  modelKey: mock-model",
				"workspaceConfig:",
				"  root: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please plan but do not execute","agentKey":"coder-app","planningMode":true}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID, confirmAwaitingID := readAwaitingQuestion(t, reader, &streamBody, "是否按此计划执行？")
	submitFrontendAnswer(t, fixture.server, runID, confirmAwaitingID, "取消执行")

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after cancel: %v", readErr)
		}
	}
	body := streamBody.String()
	if strings.Contains(body, `"type":"task.start"`) {
		t.Fatalf("did not expect task.start after cancel, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.start","toolName":"bash"`) || strings.Contains(body, `"type":"tool.start","toolName":"file_write"`) || strings.Contains(body, `"type":"tool.start","toolName":"file_edit"`) {
		t.Fatalf("did not expect mutating tool calls after cancel, got %s", body)
	}
	if !strings.Contains(body, `"status":"canceled"`) || !strings.Contains(body, "已取消执行计划。") {
		t.Fatalf("expected canceled plan update and message, got %s", body)
	}
	if got := providerCallCount.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func readAwaitingQuestion(t *testing.T, reader *bufio.Reader, streamBody *strings.Builder, expectedQuestion string) (string, string) {
	t.Helper()
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" && awaitingQuestionText(payload) == expectedQuestion {
				runID, _ := payload["runId"].(string)
				awaitingID, _ := payload["awaitingId"].(string)
				if runID == "" || awaitingID == "" {
					t.Fatalf("expected awaiting identifiers, got %#v", payload)
				}
				return runID, awaitingID
			}
		}
		if readErr == io.EOF {
			t.Fatalf("stream ended before awaiting question %q, got %s", expectedQuestion, streamBody.String())
		}
		if readErr != nil {
			t.Fatalf("read stream before awaiting question %q: %v", expectedQuestion, readErr)
		}
	}
}

func assertCoderPlanningToolSet(t *testing.T, got []string) {
	t.Helper()
	if len(got) != 5 {
		t.Fatalf("coder planning tools length=%d tools=%#v", len(got), got)
	}
	assertStringSliceContains(t, got, "file_read", "file_grep", "datetime", "ask_user_question", "plan_add_tasks")
	assertStringSliceExcludes(t, got, "bash", "file_write", "file_edit", "desktop_action", "desktop_cdp", "agent_invoke", "plan_update_task")
}

func awaitingQuestionText(payload map[string]any) string {
	questions, _ := payload["questions"].([]any)
	if len(questions) == 0 {
		return ""
	}
	first, _ := questions[0].(map[string]any)
	return strings.TrimSpace(stringValue(first["question"]))
}

func submitFrontendAnswer(t *testing.T, server http.Handler, runID string, awaitingID string, answer string) {
	t.Helper()
	body := `{"runId":"` + runID + `","awaitingId":"` + awaitingID + `","params":[{"answer":"` + answer + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !response.Data.Accepted || response.Data.Status != "accepted" {
		t.Fatalf("expected accepted submit, got %#v", response.Data)
	}
}

func assertPersistedPlanningModeRequestQuery(t *testing.T, server http.Handler) {
	t.Helper()
	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat, got %#v", chatsResp.Data)
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	for _, event := range chatResp.Data.Events {
		if event.Type == "request.query" && event.Value("planningMode") == true {
			return
		}
	}
	t.Fatalf("expected persisted request.query planningMode=true, got %#v", chatResp.Data.Events)
}

func TestQueryPersistsToolSnapshotWhenStreamToolPayloadEventsDisabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		hasToolMessage := false
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if role, _ := message["role"].(string); role == "tool" {
				hasToolMessage = true
				break
			}
		}
		if !hasToolMessage {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"datetime","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"payload hidden"}}]}`,
			`{"choices":[{"delta":{"content":" from sse"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	fixture.cfg.Stream.IncludeToolPayloadEvents = false
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          nil,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.start"`) || !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool lifecycle to remain in stream, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.args"`) || strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected live stream to exclude tool payload events, got %s", body)
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"content.snapshot",
		"run.complete",
	)
}

func TestQueryFailsRunWhenProviderOmitsToolCallID(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"datetime","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"run.error"`) {
		t.Fatalf("expected run.error when toolCallId is missing, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("did not expect tool.result when toolCallId is missing, got %s", body)
	}
	if strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("did not expect run.complete after toolCallId error, got %s", body)
	}
}

func TestQueryEmitsRunErrorOnInvalidFirstFrame(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"broken":true}`, `[DONE]`)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"bad stream"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	// LLM stream now starts after bootstrap events, so the response is
	// always SSE (200).  An invalid first frame produces run.error via SSE
	// instead of a JSON 500 — consistent with Java behaviour.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "run.start") {
		t.Fatalf("expected bootstrap events before error, got %s", body)
	}
	if !strings.Contains(body, "run.error") {
		t.Fatalf("expected run.error event, got %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected [DONE] sentinel, got %s", body)
	}
}

func TestQueryEmitsRunErrorWhenStreamFailsMidFlight(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"partial"}}]}`)
		_, _ = io.WriteString(w, "data: {not-json}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"mid stream error"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected streamed content delta, got %s", body)
	}
	if !strings.Contains(body, `"type":"run.error"`) {
		t.Fatalf("expected run.error event, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", body)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, body)
}

func TestQueryStreamsBeforeRunCompleteOverHTTP(t *testing.T) {
	if os.Getenv("RUN_SOCKET_TESTS") != "1" {
		t.Skip("set RUN_SOCKET_TESTS=1 to run real loopback SSE test")
	}
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"first "}}]}`,
			`{"choices":[{"delta":{"content":"second"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"stream please"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	seenDelta := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatalf("read sse line: %v", err)
		}
		if strings.Contains(line, `"type":"content.delta"`) {
			seenDelta = true
		}
		if strings.Contains(line, `"type":"run.complete"`) && !seenDelta {
			t.Fatalf("expected content.delta before run.complete")
		}
		if err == io.EOF {
			break
		}
	}
	if !seenDelta {
		t.Fatalf("expected to observe streamed content delta before completion")
	}
}

func TestInterruptCancelsActiveRunAndSkipsRunComplete(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		if _, err := io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"partial"}}]}`+"\n\n"); err != nil {
			t.Fatalf("write partial delta: %v", err)
		}
		flusher.Flush()
		<-r.Context().Done()
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"interrupt me"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "run.start" {
				runID, _ = payload["runId"].(string)
			}
			if payload["type"] == "content.delta" && runID != "" {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before interrupt: %v", readErr)
		}
	}

	interruptRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(interruptRec, httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"runId":"`+runID+`"}`)))
	if interruptRec.Code != http.StatusOK {
		t.Fatalf("interrupt expected 200, got %d: %s", interruptRec.Code, interruptRec.Body.String())
	}
	var interruptResp api.ApiResponse[api.InterruptResponse]
	if err := json.Unmarshal(interruptRec.Body.Bytes(), &interruptResp); err != nil {
		t.Fatalf("decode interrupt response: %v", err)
	}
	if !interruptResp.Data.Accepted || interruptResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted interrupt, got %#v", interruptResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after interrupt: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"run.cancel"`) {
		t.Fatalf("expected run.cancel event, got %s", body)
	}
	if strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("did not expect run.complete after interrupt, got %s", body)
	}

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat, got %#v", chatsResp.Data)
	}
	if chatsResp.Data[0].LastRunID != runID || chatsResp.Data[0].LastRunContent != "partial" {
		t.Fatalf("expected interrupted run to keep streamed partial summary, got %#v", chatsResp.Data[0])
	}
}
