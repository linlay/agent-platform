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

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
)

func TestQuerySSEPersistsChatHistory(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	body := bytes.NewBufferString(`{"message":"元素碳的简介，100字","agentKey":"mock-runner"}`)
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
	if len(chatResp.Data.Events) != 13 {
		t.Fatalf("expected 13 persisted events for two turns, got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
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

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hello","agentKey":"mock-runner"}`))
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

func TestQueryAndRunStreamHideDebugEventsByDefaultButPersistThem(t *testing.T) {
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
	assertEventTypesInclude(t, chatResp.Data.Events, "debug.preCall", "debug.postCall")
}

func TestQueryAndRunStreamIncludeDebugEventsWhenEnabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Stream.IncludeDebugEvents = true
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
	if _, exists := preCallData["systemPrompt"]; exists {
		t.Fatalf("did not expect systemPrompt in debug.preCall payload, got %#v", preCallData)
	}
	if _, exists := preCallData["tools"]; exists {
		t.Fatalf("did not expect tools in debug.preCall payload, got %#v", preCallData)
	}
	runID, _ := messages[0]["runId"].(string)
	if runID == "" {
		t.Fatalf("expected runId in first sse message, got %#v", messages[0])
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID, nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	assertStringSliceContains(t, decodeEventTypesFromSSE(t, runRec.Body.String()), "debug.preCall", "debug.postCall")
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
			assertStringSliceContains(t, toolNames, "datetime", "_memory_search_", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time_alpha", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 3:
			assertStringSliceContains(t, toolNames, "datetime", "_memory_search_", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_done_alpha", "plan_update_task", map[string]any{
					"taskId": "task_alpha",
					"status": "completed",
				}),
				`[DONE]`,
			)
		case 4:
			assertStringSliceContains(t, toolNames, "datetime", "_memory_search_", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "plan_add_tasks")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time_beta", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 5:
			assertStringSliceContains(t, toolNames, "datetime", "_memory_search_", "plan_update_task")
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
			cfg.Stream.IncludeDebugEvents = true
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-runner", "agent.yml")
			if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
				"key: mock-runner",
				"name: Mock Runner",
				"role: 测试代理",
				"description: plan execute test agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - datetime",
				"    - _memory_search_",
				"mode: PLAN_EXECUTE",
				"stageSettings:",
				"  maxWorkRoundsPerTask: 4",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write plan-execute agent config: %v", err)
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"请先规划再按顺序执行两个任务","agentKey":"mock-runner"}`))
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
		assertStringSliceContains(t, preCallTools[callIndex], "datetime", "_memory_search_", "plan_update_task")
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
	if chatsResp.Data[0].LastRunID != "" || chatsResp.Data[0].LastRunContent != "" {
		t.Fatalf("expected interrupted run to skip completion summary, got %#v", chatsResp.Data[0])
	}
}
