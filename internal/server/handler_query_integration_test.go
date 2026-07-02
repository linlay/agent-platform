package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
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
	if strings.Contains(bodyText, `"type":"memory.context"`) {
		t.Fatalf("did not expect memory.context in live sse, got %s", bodyText)
	}
	if !strings.Contains(bodyText, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", bodyText)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, bodyText)
	assertSSEEventOrder(t, bodyText, "chat.start", "request.query", "run.start")

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
	assertPersistedEventsStartWith(t, chatResp.Data.Events, "chat.start", "request.query", "run.start")
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

func TestQueryAppliesAgentSamplingConfigToProviderRequest(t *testing.T) {
	var sawProviderRequest atomic.Bool
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if floatValue(payload["temperature"]) != 0.4 {
			t.Fatalf("expected stage temperature override 0.4, got %#v in %#v", payload["temperature"], payload)
		}
		if floatValue(payload["top_p"]) != 0.95 {
			t.Fatalf("expected inherited top_p 0.95, got %#v in %#v", payload["top_p"], payload)
		}
		if floatValue(payload["presence_penalty"]) != 0 {
			t.Fatalf("expected inherited presence_penalty 0, got %#v in %#v", payload["presence_penalty"], payload)
		}
		if floatValue(payload["frequency_penalty"]) != 0.15 {
			t.Fatalf("expected inherited frequency_penalty 0.15, got %#v in %#v", payload["frequency_penalty"], payload)
		}
		if floatValue(payload["seed"]) != 123 {
			t.Fatalf("expected inherited seed 123, got %#v in %#v", payload["seed"], payload)
		}
		sawProviderRequest.Store(true)
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"sampled response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatalf("read agent config: %v", err)
			}
			content := strings.Replace(string(data),
				"modelConfig:\n  modelKey: mock-model\n",
				"modelConfig:\n  modelKey: mock-model\n  sampling:\n    temperature: 0.7\n    topP: 0.95\n    presencePenalty: 0\n    frequencyPenalty: 0.15\n    seed: 123\n",
				1,
			)
			content = strings.TrimSpace(content) + "\n" +
				"stageSettings:\n" +
				"  execute:\n" +
				"    modelConfig:\n" +
				"      sampling:\n" +
				"        temperature: 0.4\n"
			if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
				t.Fatalf("write sampled agent config: %v", err)
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !sawProviderRequest.Load() {
		t.Fatal("expected provider request")
	}
}

func TestQueryNonStreamReturnsJSONAndPersistsChatHistory(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"JSON "}}]}`,
			`{"choices":[{"delta":{"content":"response"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	})
	server := fixture.server

	chatID := "chat-nonstream-json"
	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","message":"非流式回答","agentKey":"mock-agent","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected json content type, got %q", got)
	}
	if strings.Contains(rec.Body.String(), "data:") || strings.Contains(rec.Body.String(), stream.DoneSentinel) {
		t.Fatalf("did not expect sse body, got %s", rec.Body.String())
	}

	var rawResp api.ApiResponse[map[string]json.RawMessage]
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("decode raw query response: %v", err)
	}
	if len(rawResp.Data) != 1 {
		t.Fatalf("expected content-only response data, got %s", rec.Body.String())
	}
	if _, ok := rawResp.Data["content"]; !ok {
		t.Fatalf("expected content field, got %s", rec.Body.String())
	}
	if _, ok := rawResp.Data["assistantText"]; ok {
		t.Fatalf("did not expect assistantText in default response, got %s", rec.Body.String())
	}
	if _, ok := rawResp.Data["usage"]; ok {
		t.Fatalf("did not expect usage in default response, got %s", rec.Body.String())
	}

	var queryResp api.ApiResponse[api.QueryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &queryResp); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if queryResp.Code != 0 || queryResp.Data.Content != "JSON response" {
		t.Fatalf("unexpected query response %#v", queryResp)
	}
	if queryResp.Data.Usage != nil || queryResp.Data.FullText != nil {
		t.Fatalf("did not expect optional fields by default, got %#v", queryResp.Data)
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"content.snapshot",
		"run.complete",
	)
	if len(chatResp.Data.Runs) == 0 || chatResp.Data.Runs[0].AssistantText != "JSON response" {
		t.Fatalf("expected run summary assistant text, got %#v", chatResp.Data.Runs)
	}
	if chatResp.Data.Usage == nil || chatResp.Data.Usage.LastRun == nil || chatResp.Data.Usage.LastRun.TotalTokens != 10 {
		t.Fatalf("expected persisted usage breakdown, got %#v", chatResp.Data.Usage)
	}
}

func TestQueryNonStreamReturnsFailureWhenRunErrors(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"broken":true}`, `[DONE]`)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"bad stream","agentKey":"mock-agent","stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if resp.Code != http.StatusInternalServerError || !strings.Contains(resp.Msg, "provider stream returned no choices") {
		t.Fatalf("expected provider error failure, got %#v body=%s", resp, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"content"`) {
		t.Fatalf("did not expect success content field on run error, got %s", rec.Body.String())
	}
}

func TestQuerySteamTypoDoesNotDisableSSE(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	body := bytes.NewBufferString(`{"message":"拼写错误字段","agentKey":"mock-agent","steam":false}`)
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
	if !strings.Contains(rec.Body.String(), "data: "+stream.DoneSentinel) {
		t.Fatalf("expected sse done sentinel, got %s", rec.Body.String())
	}
}

func TestQueryRejectsInvalidAccessLevel(t *testing.T) {
	fixture := newTestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hello","accessLevel":"root"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "accessLevel") {
		t.Fatalf("expected accessLevel validation error, got %s", rec.Body.String())
	}
}

func TestQueryRoleValidation(t *testing.T) {
	fixture := newTestFixture(t)
	for _, role := range []string{"", "user", "assistant", "automation", "system"} {
		body := `{"message":"hello"}`
		if role != "" {
			body = `{"message":"hello","role":"` + role + `"}`
		}
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(body))
		admission, err := fixture.server.prepareQueryAdmission(req, true)
		if err != nil {
			t.Fatalf("role %q should be accepted: %v", role, err)
		}
		want := role
		if want == "" {
			want = api.QueryRoleUser
		}
		if admission.req.Role != want {
			t.Fatalf("role %q normalized to %q, want %q", role, admission.req.Role, want)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hello","role":"scheduler"}`))
	_, err := fixture.server.prepareQueryAdmission(req, true)
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusBadRequest || !strings.Contains(statusErr.message, "role must be") {
		t.Fatalf("expected invalid role 400, got %#v", err)
	}
}

func TestQueryAvailabilityRouteRemoved(t *testing.T) {
	fixture := newTestFixture(t)

	availabilityRec := httptest.NewRecorder()
	availabilityReq := httptest.NewRequest(http.MethodPost, "/api/query/availability", bytes.NewBufferString(`{"agentKey":"mock-agent","chatId":"chat-next"}`))
	availabilityReq.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(availabilityRec, availabilityReq)
	if availabilityRec.Code != http.StatusNotFound {
		t.Fatalf("expected query availability route to be removed, got %d: %s", availabilityRec.Code, availabilityRec.Body.String())
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
	requestQuery := findSSEMessageByType(t, messages, "request.query")
	if requestQuery["runId"] != runID {
		t.Fatalf("expected request.query to carry provided run id, got %#v", requestQuery)
	}
	runStart := findSSEMessageByType(t, messages, "run.start")
	if runStart["runId"] != runID {
		t.Fatalf("expected run.start to carry provided run id, got %#v", runStart)
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
		"references":[{"id":"ref_1","type":"file","name":"notes.txt","path":"/tmp/notes.txt","mimeType":"text/plain"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	messages := decodeSSEMessages(t, rec.Body.String())
	assertRequestQueryContext(t, findSSEMessageByType(t, messages, "request.query"))

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
	if reference["path"] != "/tmp/notes.txt" {
		t.Fatalf("expected request.query reference path, got %#v", reference)
	}
}

func TestQueryRejectsRemovedSandboxPathReferenceField(t *testing.T) {
	fixture := newTestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"message":"bad reference",
		"references":[{"id":"ref_1","sandboxPath":"/workspace/notes.txt"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), api.ReferenceSandboxPathRemovedMessage) {
		t.Fatalf("expected removed field message, got %s", rec.Body.String())
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

func TestChatSnapshotDeduplicatesChatStartAcrossMultipleQueries(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runtime test response"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	})
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
	usageSnapshotCount := 0
	prevSeq := int64(0)
	for _, event := range chatResp.Data.Events {
		eventType := event.Type
		switch eventType {
		case "chat.start":
			chatStartCount++
		case "run.start":
			runStartCount++
		case "usage.snapshot":
			usageSnapshotCount++
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
	if usageSnapshotCount != 0 {
		t.Fatalf("expected no historical usage.snapshot events, got %d events=%#v", usageSnapshotCount, chatResp.Data.Events)
	}
	if len(chatResp.Data.Events) != 8 && len(chatResp.Data.Events) != 9 {
		t.Fatalf("expected 8 or 9 persisted events for two turns, got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
	}
	assertPersistedEventsStartWith(t, chatResp.Data.Events,
		"chat.start",
		"request.query",
		"run.start",
		"content.snapshot",
		"run.complete",
		"request.query",
		"run.start",
		"content.snapshot",
	)
	if len(chatResp.Data.Events) == 9 && chatResp.Data.Events[8].Type != "run.complete" {
		t.Fatalf("expected final event to be run.complete when active run is already finished, got %#v", chatResp.Data.Events[8])
	}
	if len(chatResp.Data.RawMessages) != 4 {
		t.Fatalf("expected four raw messages for two turns, got %#v", chatResp.Data.RawMessages)
	}
	if chatResp.Data.Usage == nil || chatResp.Data.Usage.LastRun == nil || chatResp.Data.Usage.Chat == nil {
		t.Fatalf("expected outer usage breakdown, got %#v", chatResp.Data.Usage)
	}
	if chatResp.Data.ContextWindow == nil || chatResp.Data.ContextWindow.MaxSize == 0 || chatResp.Data.ContextWindow.CurrentSize == 0 {
		t.Fatalf("expected outer context window, got %#v", chatResp.Data.ContextWindow)
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

func TestQueryNonStreamCanExecuteBackendToolLoop(t *testing.T) {
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
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`,
			`[DONE]`,
		)
	})
	server := fixture.server

	chatID := "chat-nonstream-tool"
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"现在几点？","stream":false,"includeUsage":true,"includeFullText":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data:") || strings.Contains(rec.Body.String(), stream.DoneSentinel) {
		t.Fatalf("did not expect sse body, got %s", rec.Body.String())
	}
	var queryResp api.ApiResponse[api.QueryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &queryResp); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if queryResp.Data.Content != "完成工具调用后的最终回答" {
		t.Fatalf("unexpected query response %#v", queryResp)
	}
	if queryResp.Data.Usage == nil || queryResp.Data.Usage.TotalTokens != 16 {
		t.Fatalf("expected run usage in response, got %#v", queryResp.Data.Usage)
	}
	if queryResp.Data.FullText == nil {
		t.Fatalf("expected fullText in response, got %#v", queryResp.Data)
	}
	fullText := *queryResp.Data.FullText
	if !strings.Contains(fullText, "datetime") || !strings.Contains(fullText, "Tool result") || !strings.Contains(fullText, "完成工具调用后的最终回答") {
		t.Fatalf("expected tool process and final answer in fullText, got %q", fullText)
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
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

func TestQuerySendsPlaintextProviderAPIKeyAuthorizationHeader(t *testing.T) {
	const plainAPIKey = "test-key"

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+plainAPIKey {
			t.Fatalf("expected Authorization header, got %q", got)
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
				"apiKey: " + plainAPIKey,
				"defaultModel: mock-model",
			}, "\n")
			providerPath := filepath.Join(root, "registries", "providers", "mock.yml")
			if err := os.WriteFile(providerPath, []byte(providerConfig), 0o644); err != nil {
				t.Fatalf("write provider config: %v", err)
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
	sseTypes := decodeEventTypesFromSSE(t, body)
	assertStringSliceExcludes(t, sseTypes, "debug.llmChat")
	assertStringSliceContains(t, sseTypes, "usage.snapshot")

	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got %s", body)
	}
	requestQuery := findSSEMessageByType(t, messages, "request.query")
	runID, _ := requestQuery["runId"].(string)
	chatID, _ := requestQuery["chatId"].(string)
	if runID == "" || chatID == "" {
		t.Fatalf("expected runId/chatId in request.query sse message, got %#v", requestQuery)
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID+"&agentKey=mock-agent", nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	runTypes := decodeEventTypesFromSSE(t, runRec.Body.String())
	assertStringSliceExcludes(t, runTypes, "debug.llmChat")
	assertStringSliceContains(t, runTypes, "usage.snapshot")

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertEventTypesExclude(t, chatResp.Data.Events, "debug.llmChat")
	assertEventTypesExclude(t, chatResp.Data.Events, "usage.snapshot")
	if chatResp.Data.Usage == nil || chatResp.Data.ContextWindow == nil {
		t.Fatalf("expected outer usage and context window, got usage=%#v contextWindow=%#v", chatResp.Data.Usage, chatResp.Data.ContextWindow)
	}
}

func TestQueryLLMChatRecordEmitsDebugLLMChat(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Logging.LLMInteraction.RecordEnabled = true
			cfg.Logging.LLMInteraction.RecordDir = cfg.Paths.ChatsDir
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"record debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	sseTypes := decodeEventTypesFromSSE(t, body)
	assertStringSliceContains(t, sseTypes, "debug.llmChat")

	messages := decodeSSEMessages(t, body)
	var llmChat map[string]any
	for _, message := range messages {
		if stringValue(message["type"]) == "debug.llmChat" {
			llmChat = message
			break
		}
	}
	if llmChat == nil {
		t.Fatalf("expected debug.llmChat message, got %#v", messages)
	}
	requestQuery := findSSEMessageByType(t, messages, "request.query")
	runID, _ := requestQuery["runId"].(string)
	chatID, _ := requestQuery["chatId"].(string)
	data, _ := llmChat["data"].(map[string]any)
	if data["status"] != "ok" || testIntValue(data["runSeq"]) != 1 {
		t.Fatalf("unexpected debug.llmChat metadata %#v", data)
	}
	traceInfo, _ := data["trace"].(map[string]any)
	traceFile, _ := traceInfo["file"].(string)
	traceURL, _ := traceInfo["url"].(string)
	if traceFile != chatID+"/.llm-records/"+runID+"_001.json" || traceURL == "" {
		t.Fatalf("unexpected trace payload %#v", data)
	}
	if _, exists := data["requestBody"]; exists {
		t.Fatalf("did not expect full request body in debug.llmChat payload, got %#v", data)
	}
	usage, _ := data["usage"].(map[string]any)
	llmUsage, _ := usage["llmReturnUsage"].(map[string]any)
	if testIntValue(llmUsage["promptTokens"]) != 7 || testIntValue(llmUsage["completionTokens"]) != 3 || testIntValue(llmUsage["totalTokens"]) != 10 {
		t.Fatalf("unexpected debug.llmChat usage %#v", data)
	}

	resourceRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(resourceRec, httptest.NewRequest(http.MethodGet, traceURL, nil))
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected trace resource 200, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
	var trace map[string]any
	if err := json.Unmarshal(resourceRec.Body.Bytes(), &trace); err != nil {
		t.Fatalf("decode trace json: %v body=%s", err, resourceRec.Body.String())
	}
	if trace["runId"] != runID || trace["chatId"] != chatID || trace["status"] != "ok" {
		t.Fatalf("unexpected trace metadata %#v", trace)
	}
	if _, ok := trace["request"].(map[string]any); !ok {
		t.Fatalf("expected full request in trace json, got %#v", trace)
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID+"&agentKey=mock-agent", nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	runTypes := decodeEventTypesFromSSE(t, runRec.Body.String())
	assertStringSliceContains(t, runTypes, "debug.llmChat")

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertEventTypesExclude(t, chatResp.Data.Events, "debug.llmChat")
	if chatResp.Data.Usage == nil || chatResp.Data.ContextWindow == nil {
		t.Fatalf("expected outer usage and context window, got usage=%#v contextWindow=%#v", chatResp.Data.Usage, chatResp.Data.ContextWindow)
	}
}

func testIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
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
	preTaskToolNames := make([]string, 0)
	firstTaskStartIndex := -1
	taskStarts := map[string]int{}
	taskStartPayloads := map[string]map[string]any{}
	taskCompletes := map[string]int{}

	for index, message := range messages {
		switch stringValue(message["type"]) {
		case "tool.start":
			if firstTaskStartIndex < 0 {
				preTaskToolNames = append(preTaskToolNames, stringValue(message["toolName"]))
			}
		case "task.start":
			if firstTaskStartIndex < 0 {
				firstTaskStartIndex = index
			}
			taskID := stringValue(message["taskId"])
			taskStarts[taskID] = index
			taskStartPayloads[taskID] = message
		case "task.complete":
			taskCompletes[stringValue(message["taskId"])] = index
		}
	}

	if !reflect.DeepEqual(preTaskToolNames, []string{"plan_add_tasks"}) {
		t.Fatalf("expected only plan_add_tasks before first task.start, got %#v", preTaskToolNames)
	}

	alphaStart, ok := taskStarts["task_alpha"]
	if !ok {
		t.Fatalf("missing task.start for task_alpha in %#v", messages)
	}
	if _, ok := taskStartPayloads["task_alpha"]["toolId"]; ok {
		t.Fatalf("did not expect task.start toolId, got %#v", taskStartPayloads["task_alpha"])
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
				providerToolCallsFrame(t, []providerToolCallSpec{
					{
						ID:   "tool_plan",
						Name: "finalize_planning",
						Args: map[string]any{
							"markdown": `# Confirm Coder Plan

## Summary
Plan first, then check the current time before reporting.

## Public Events And Storage
- Keep planning delta events before confirmation

## Implementation Changes
- Check the current time before reporting

## Interfaces
- Use datetime after confirmation

## Test Plan
- Verify the stream completes

## Assumptions
- The user confirms before execution starts
`,
						},
					},
					{
						ID:   "tool_plan_time",
						Name: "datetime",
						Args: map[string]any{},
					},
				}),
				`[DONE]`,
			)
		case 4:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "plan_add_tasks", "plan_get_tasks", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "finalize_planning", "ask_user_question")
			assertProviderMessagesContainToolResult(t, payload, "tool_plan", "finalize_planning", "approve")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_time", "datetime", map[string]any{}),
				`[DONE]`,
			)
		case 5:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "plan_add_tasks", "plan_get_tasks", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "finalize_planning", "ask_user_question")
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"execution completed"},"finish_reason":"stop"}]}`,
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
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	chatID := "chat_coder_plan_confirm"
	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please plan first","agentKey":"coder-app","chatId":"`+chatID+`","planningMode":true}`))
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
	_, confirmAwaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	if strings.Contains(streamBody.String(), `"type":"task.start"`) {
		t.Fatalf("did not expect task.start before plan confirmation, got %s", streamBody.String())
	}
	if !strings.Contains(streamBody.String(), `"type":"planning.delta"`) {
		t.Fatalf("expected planning.delta before confirmation, got %s", streamBody.String())
	}
	if strings.Contains(streamBody.String(), `"toolName":"finalize_planning"`) || strings.Contains(streamBody.String(), `"toolId":"tool_plan"`) {
		t.Fatalf("did not expect hidden finalize_planning tool events before confirmation, got %s", streamBody.String())
	}
	if got := strings.Count(streamBody.String(), `"type":"planning.delta"`); got <= 1 {
		t.Fatalf("expected multiple planning.delta events before confirmation, got %d in %s", got, streamBody.String())
	}
	timeResultIndex := strings.Index(streamBody.String(), `"type":"tool.result","toolId":"tool_plan_time"`)
	confirmationAskIndex := strings.Index(streamBody.String(), `"awaitingId":"`+confirmAwaitingID+`"`)
	if !(timeResultIndex >= 0 && confirmationAskIndex > timeResultIndex) {
		t.Fatalf("expected later planning batch tool result before confirmation, got %s", streamBody.String())
	}
	if strings.Contains(streamBody.String(), `"type":"planning.snapshot"`) {
		t.Fatalf("did not expect live planning.snapshot, got %s", streamBody.String())
	}
	assertPlanningLifecycleBeforePlanAwaiting(t, streamBody.String(), runID)
	planningFile := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID, chat.ToolRootDirName, chat.ToolPlansDirName, runID+"_planning_1.md")
	planningBytes, readPlanningErr := os.ReadFile(planningFile)
	if readPlanningErr != nil {
		t.Fatalf("expected planning markdown file before confirmation: %v", readPlanningErr)
	}
	if planningText := string(planningBytes); !strings.Contains(planningText, "# Confirm Coder Plan") ||
		!strings.Contains(planningText, "## Test Plan") {
		t.Fatalf("unexpected planning markdown file:\n%s", planningText)
	}
	submitFrontendDecision(t, fixture.server, runID, confirmAwaitingID, "approve")

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
	planIndex := strings.LastIndex(body, `"type":"planning.delta"`)
	confirmIndex := strings.Index(body, `"mode":"plan"`)
	completeIndex := strings.LastIndex(body, `"type":"run.complete","runId":"`+runID+`"`)
	if !(planIndex >= 0 && confirmIndex > planIndex && completeIndex > confirmIndex) {
		t.Fatalf("expected planning.delta before confirmation and old run.complete after confirmation, got %s", body)
	}
	if !strings.Contains(body, `"mode":"plan"`) || !strings.Contains(body, `"decision":"approve"`) {
		t.Fatalf("expected confirmation plan answer in stream, got %s", body)
	}
	if strings.Contains(body, `"type":"planning.snapshot"`) {
		t.Fatalf("did not expect live planning.snapshot, got %s", body)
	}
	assertPlanningLifecycleBeforePlanAwaiting(t, body, runID)
	if got := strings.Count(body, `"type":"planning.delta"`); got <= 1 {
		t.Fatalf("expected multiple live planning.delta events, got %d in %s", got, body)
	}
	if strings.Contains(body, "execution completed") {
		t.Fatalf("did not expect execution content bridged into planning stream, got %s", body)
	}
	if strings.Contains(body, "confirmed plan completed") || strings.Contains(body, "coder-summary") {
		t.Fatalf("did not expect a separate coder summary stage, got %s", body)
	}
	executionRunID := handoffRunIDAfterComplete(t, body, runID, chatID, "coder-app")
	executionBody := attachRunSSE(t, httpServer.URL, "coder-app", executionRunID)
	assertAttachedCoderExecuteRun(t, executionBody, executionRunID, chatID, "coder-app", "execution completed")
	if got := providerCallCount.Load(); got != 5 {
		t.Fatalf("provider calls = %d, want 5", got)
	}
	assertPersistedPlanningModeRequestQuery(t, fixture.server)
	assertJSONLFinalizePlanningHistory(t, fixture.chats, chatID, map[string]string{"tool_plan": "approve"})
	assertJSONLCoderExecuteSyntheticQuery(t, fixture.chats, chatID, "Execute plan")
}

func TestFinalizePlanningStreamsDeltasBeforeProviderFinishes(t *testing.T) {
	var providerCallCount atomic.Int32
	firstFrameFlushed := make(chan struct{})
	continueProvider := make(chan struct{})
	releasedProvider := false
	defer func() {
		if !releasedProvider {
			close(continueProvider)
		}
	}()

	prefixArgs := `{"markdown":"# Streaming Plan\n\n## Summary\nPart one`
	suffixArgs := ` and part two.\n\n## Test Plan\n- Verify true streaming\n"}`

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		switch call := providerCallCount.Add(1); call {
		case 1:
			assertCoderPlanningToolSet(t, providerRequestToolNames(payload["tools"]))

			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("expected flusher")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			writeProviderSSEFrame(t, w, providerToolCallArgsDeltaFrame(t, "tool_plan", "finalize_planning", prefixArgs, ""))
			flusher.Flush()
			close(firstFrameFlushed)

			select {
			case <-continueProvider:
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting to continue provider stream")
			}

			writeProviderSSEFrame(t, w, providerToolCallArgsDeltaFrame(t, "", "", suffixArgs, "tool_calls"))
			flusher.Flush()
			writeProviderSSEFrame(t, w, `[DONE]`)
			flusher.Flush()
		case 2:
			assertCoderPlanningToolSet(t, providerRequestToolNames(payload["tools"]))
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"已取消执行计划。"},"finish_reason":"stop"}]}`,
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
			lines := []string{
				"key: coder-app",
				"name: Coder App",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	client := http.Client{Timeout: 10 * time.Second}
	chatID := "chat_stream_plan_hidden"
	resp, err := client.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please stream the plan","agentKey":"coder-app","chatId":"`+chatID+`","planningMode":true}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	select {
	case <-firstFrameFlushed:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for first provider frame")
	}

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID, firstDelta := readUntilPlanningDelta(t, reader, &streamBody)
	if !strings.Contains(firstDelta, "Part one") {
		t.Fatalf("expected first planning delta from partial provider args, got %q in %s", firstDelta, streamBody.String())
	}
	if strings.Contains(streamBody.String(), `"type":"planning.end"`) {
		t.Fatalf("did not expect planning.end before provider finished, got %s", streamBody.String())
	}
	assertPlanningStartBeforeDelta(t, streamBody.String())
	if strings.Contains(streamBody.String(), `"type":"planning.snapshot"`) {
		t.Fatalf("did not expect live planning.snapshot, got %s", streamBody.String())
	}
	planningFile := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID, chat.ToolRootDirName, chat.ToolPlansDirName, runID+"_planning_1.md")
	draftBytes, readDraftErr := os.ReadFile(planningFile)
	if readDraftErr != nil {
		t.Fatalf("expected draft planning file before provider finished: %v", readDraftErr)
	}
	draft := string(draftBytes)
	if !strings.Contains(draft, "Part one") || strings.Contains(draft, "part two") {
		t.Fatalf("unexpected draft planning markdown before provider finished:\n%s", draft)
	}
	assertFinalizePlanningToolVisibility(t, streamBody.String(), false)

	prefixDeltaCount := strings.Count(streamBody.String(), `"type":"planning.delta"`)
	close(continueProvider)
	releasedProvider = true

	_, confirmAwaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	bodyBeforeDecision := streamBody.String()
	assertPlanningLifecycleBeforePlanAwaiting(t, bodyBeforeDecision, runID)
	if got := strings.Count(bodyBeforeDecision, `"type":"planning.delta"`); got <= prefixDeltaCount {
		t.Fatalf("expected additional planning.delta after provider finished, got %d before=%d in %s", got, prefixDeltaCount, bodyBeforeDecision)
	}
	finalBytes, readFinalErr := os.ReadFile(planningFile)
	if readFinalErr != nil {
		t.Fatalf("read final planning file: %v", readFinalErr)
	}
	if !strings.Contains(string(finalBytes), "Part one and part two.") {
		t.Fatalf("expected final planning markdown to contain both provider chunks, got:\n%s", string(finalBytes))
	}
	assertFinalizePlanningToolVisibility(t, bodyBeforeDecision, false)

	submitFrontendDecision(t, fixture.server, runID, confirmAwaitingID, "reject")
	readRemainingSSEUntilEOF(t, reader, &streamBody)
	if got := providerCallCount.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
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
				providerToolCallFrame(t, "tool_plan", "finalize_planning", map[string]any{
					"markdown": `# Cancel Coder Plan

## Summary
Plan should be canceled before execution.

## Public Events And Storage
- No execution events should start

## Implementation Changes
- This task must not start

## Interfaces
- Use planning confirmation before execution

## Test Plan
- Ensure no mutating tool is called

## Assumptions
- The user cancels at confirmation
`,
				}),
				`[DONE]`,
			)
		case 2:
			assertCoderPlanningToolSet(t, providerRequestToolNames(payload["tools"]))
			assertProviderMessagesContainToolResult(t, payload, "tool_plan", "finalize_planning", "reject")
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"已取消执行计划。"},"finish_reason":"stop"}]}`,
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
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
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
	runID, confirmAwaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	submitFrontendDecision(t, fixture.server, runID, confirmAwaitingID, "reject")

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
	if !strings.Contains(body, "已取消执行计划。") {
		t.Fatalf("expected canceled plan message, got %s", body)
	}
	answerIndex := strings.Index(body, `"type":"awaiting.answer"`)
	if answerIndex < 0 {
		t.Fatalf("expected awaiting.answer after cancel, got %s", body)
	}
	if planningEndIndex := strings.Index(body[answerIndex:], `"type":"planning.end"`); planningEndIndex >= 0 {
		t.Fatalf("did not expect planning.end after awaiting.answer cancel, got %s", body)
	}
	if got := providerCallCount.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	assertPersistedPlanningModeRequestQuery(t, fixture.server)
}

func TestCoderPlanningModeRejectCanGenerateRevisionAndApprove(t *testing.T) {
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
				providerToolCallFrame(t, "tool_plan_v1", "finalize_planning", map[string]any{
					"markdown": `# Revision Plan V1

## Summary
Initial plan with too little test coverage.

## Public Events And Storage
- Emit the first planning revision

## Implementation Changes
- Implement the feature

## Interfaces
- Use mode=plan

## Test Plan
- Minimal tests

## Assumptions
- The user may request changes
`,
				}),
				`[DONE]`,
			)
		case 2:
			assertCoderPlanningToolSet(t, toolNames)
			if !strings.Contains(string(mustJSONMarshal(t, payload)), "请补充测试范围") {
				t.Fatalf("expected feedback reason in planning feedback prompt, got %#v", payload)
			}
			assertProviderMessagesContainToolResult(t, payload, "tool_plan_v1", "finalize_planning", "reject")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan_v2", "finalize_planning", map[string]any{
					"markdown": `# Revision Plan V2

## Summary
Revised plan with explicit test coverage.

## Public Events And Storage
- Emit the second planning revision

## Implementation Changes
- Implement the feature
- Preserve rejected plan as non-executable

## Interfaces
- Use mode=plan

## Test Plan
- Cover approve
- Cover reject
- Cover revision

## Assumptions
- The user approves the revised plan
`,
				}),
				`[DONE]`,
			)
		case 3:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "plan_add_tasks", "plan_get_tasks", "plan_update_task")
			assertStringSliceExcludes(t, toolNames, "finalize_planning", "ask_user_question")
			assertProviderMessagesContainToolResult(t, payload, "tool_plan_v2", "finalize_planning", "approve")
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"executed revised plan"},"finish_reason":"stop"}]}`,
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
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	chatID := "chat_plan_revisions"
	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please plan revisions","agentKey":"coder-app","chatId":"`+chatID+`","planningMode":true}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID, firstConfirmAwaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	if !strings.Contains(streamBody.String(), runID+`_planning_1`) || !strings.Contains(streamBody.String(), `"awaitingId":"tool_plan_v1"`) {
		t.Fatalf("expected first planning revision and confirmation, got %s", streamBody.String())
	}
	submitFrontendDecisionWithReason(t, fixture.server, runID, firstConfirmAwaitingID, "reject", "请补充测试范围")

	_, secondConfirmAwaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	if !strings.Contains(streamBody.String(), runID+`_planning_2`) || !strings.Contains(streamBody.String(), `"awaitingId":"tool_plan_v2"`) {
		t.Fatalf("expected second planning revision and confirmation, got %s", streamBody.String())
	}
	if got := countRunStartsForDifferentRun(t, streamBody.String(), runID); got != 0 {
		t.Fatalf("reject should not start a new run before revised plan approval, got %d handoff starts in %s", got, streamBody.String())
	}
	secondPlanningFile := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID, chat.ToolRootDirName, chat.ToolPlansDirName, runID+"_planning_2.md")
	secondPlanningBytes, readErr := os.ReadFile(secondPlanningFile)
	if readErr != nil {
		t.Fatalf("expected second planning markdown file: %v", readErr)
	}
	if !strings.Contains(string(secondPlanningBytes), "# Revision Plan V2") || !strings.Contains(string(secondPlanningBytes), "Cover revision") {
		t.Fatalf("unexpected second planning markdown:\n%s", string(secondPlanningBytes))
	}
	submitFrontendDecision(t, fixture.server, runID, secondConfirmAwaitingID, "approve")
	readRemainingSSEUntilEOF(t, reader, &streamBody)

	body := streamBody.String()
	if strings.Contains(body, "executed revised plan") {
		t.Fatalf("did not expect revised plan execution bridged into planning stream, got %s", body)
	}
	if strings.Contains(body, "summary for revised plan") || strings.Contains(body, "coder-summary") {
		t.Fatalf("did not expect a separate coder summary stage, got %s", body)
	}
	executionRunID := handoffRunIDAfterComplete(t, body, runID, chatID, "coder-app")
	executionBody := attachRunSSE(t, httpServer.URL, "coder-app", executionRunID)
	assertAttachedCoderExecuteRun(t, executionBody, executionRunID, chatID, "coder-app", "executed revised plan")
	if got := providerCallCount.Load(); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	assertJSONLFinalizePlanningHistory(t, fixture.chats, chatID, map[string]string{
		"tool_plan_v1": "reject",
		"tool_plan_v2": "approve",
	})
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

func readAwaitingApproval(t *testing.T, reader *bufio.Reader, streamBody *strings.Builder, expectedApprovalID string) (string, string) {
	t.Helper()
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" && awaitingApprovalID(payload) == expectedApprovalID {
				if payload["mode"] != "plan" || payload["viewportType"] != "builtin" || payload["viewportKey"] != "plan" {
					t.Fatalf("expected plan awaiting.ask, got %#v", payload)
				}
				if timeout, ok := payload["timeout"].(float64); !ok || timeout != 0 {
					t.Fatalf("expected planning confirmation timeout 0, got %#v", payload)
				}
				runID, _ := payload["runId"].(string)
				awaitingID, _ := payload["awaitingId"].(string)
				if runID == "" || awaitingID == "" {
					t.Fatalf("expected awaiting identifiers, got %#v", payload)
				}
				return runID, awaitingID
			}
		}
		if readErr == io.EOF {
			t.Fatalf("stream ended before awaiting approval %q, got %s", expectedApprovalID, streamBody.String())
		}
		if readErr != nil {
			t.Fatalf("read stream before awaiting approval %q: %v", expectedApprovalID, readErr)
		}
	}
}

func readUntilPlanningDelta(t *testing.T, reader *bufio.Reader, streamBody *strings.Builder) (string, string) {
	t.Helper()
	runID := ""
	for {
		line, readErr := readSSELineWithTimeout(t, reader, "planning.delta")
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "request.query", "run.start":
				runID, _ = payload["runId"].(string)
			case "planning.delta":
				delta, _ := payload["delta"].(string)
				if runID == "" {
					t.Fatalf("expected planning runId before delta, got %#v", payload)
				}
				return runID, delta
			}
		}
		if readErr == io.EOF {
			t.Fatalf("stream ended before planning.delta, got %s", streamBody.String())
		}
		if readErr != nil {
			t.Fatalf("read stream before planning.delta: %v", readErr)
		}
	}
}

func assertPlanningStartBeforeDelta(t *testing.T, body string) {
	t.Helper()
	startIndex := strings.Index(body, `"type":"planning.start"`)
	deltaIndex := strings.Index(body, `"type":"planning.delta"`)
	if !(startIndex >= 0 && deltaIndex > startIndex) {
		t.Fatalf("expected planning.start before planning.delta, got %s", body)
	}
}

func assertPlanningLifecycleBeforePlanAwaiting(t *testing.T, body string, runID string) {
	t.Helper()
	startIndex := strings.Index(body, `"type":"planning.start"`)
	deltaIndex := strings.Index(body, `"type":"planning.delta"`)
	endIndex := strings.LastIndex(body, `"type":"planning.end"`)
	awaitingIndex := strings.Index(body, `"mode":"plan"`)
	if !(startIndex >= 0 && deltaIndex > startIndex && endIndex > deltaIndex && awaitingIndex > endIndex) {
		t.Fatalf("expected planning.start < planning.delta < planning.end < plan awaiting, got %s", body)
	}
}

func readRemainingSSEUntilEOF(t *testing.T, reader *bufio.Reader, streamBody *strings.Builder) {
	t.Helper()
	for {
		line, readErr := readSSELineWithTimeout(t, reader, "stream EOF")
		streamBody.WriteString(line)
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
			t.Fatalf("read remaining stream: %v", readErr)
		}
	}
}

func handoffRunIDAfterComplete(t *testing.T, body string, oldRunID string, wantChatID string, wantAgentKey string) string {
	t.Helper()
	seenOldComplete := false
	for _, message := range decodeSSEMessages(t, body) {
		eventType := stringValue(message["type"])
		runID := stringValue(message["runId"])
		if eventType == "run.complete" && runID == oldRunID {
			seenOldComplete = true
			continue
		}
		if !seenOldComplete || eventType != "run.start" || runID == "" || runID == oldRunID {
			continue
		}
		if got := stringValue(message["chatId"]); got != wantChatID {
			t.Fatalf("handoff run.start chatId = %q, want %q in %#v", got, wantChatID, message)
		}
		if got := stringValue(message["agentKey"]); got != wantAgentKey {
			t.Fatalf("handoff run.start agentKey = %q, want %q in %#v", got, wantAgentKey, message)
		}
		return runID
	}
	t.Fatalf("expected new run.start after old run.complete for %s, got %s", oldRunID, body)
	return ""
}

func countRunStartsForDifferentRun(t *testing.T, body string, oldRunID string) int {
	t.Helper()
	count := 0
	for _, message := range decodeSSEMessages(t, body) {
		if message["type"] == "run.start" {
			runID := stringValue(message["runId"])
			if runID != "" && runID != oldRunID {
				count++
			}
		}
	}
	return count
}

func attachRunSSE(t *testing.T, baseURL string, agentKey string, runID string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/attach?agentKey=" + agentKey + "&runId=" + runID)
	if err != nil {
		t.Fatalf("attach run %s: %v", runID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("attach run %s expected 200, got %d: %s", runID, resp.StatusCode, string(data))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read attach run %s: %v", runID, err)
	}
	return string(data)
}

func assertAttachedCoderExecuteRun(t *testing.T, body string, runID string, chatID string, agentKey string, wantContent string) {
	t.Helper()
	if !strings.Contains(body, wantContent) {
		t.Fatalf("expected attached execution content %q, got %s", wantContent, body)
	}
	if !strings.Contains(body, `"source":"coder-plan-approve"`) || !strings.Contains(body, `"stage":"coder-execute"`) {
		t.Fatalf("expected attached synthetic execute query metadata, got %s", body)
	}
	foundStart := false
	foundComplete := false
	for _, message := range decodeSSEMessages(t, body) {
		if stringValue(message["runId"]) != runID {
			continue
		}
		switch message["type"] {
		case "run.start":
			foundStart = true
			if got := stringValue(message["chatId"]); got != chatID {
				t.Fatalf("attached run.start chatId = %q, want %q in %#v", got, chatID, message)
			}
			if got := stringValue(message["agentKey"]); got != agentKey {
				t.Fatalf("attached run.start agentKey = %q, want %q in %#v", got, agentKey, message)
			}
		case "run.complete":
			foundComplete = true
		}
	}
	if !foundStart || !foundComplete {
		t.Fatalf("expected attached run.start and run.complete for %s, got %s", runID, body)
	}
}

func readSSELineWithTimeout(t *testing.T, reader *bufio.Reader, waitingFor string) (string, error) {
	t.Helper()
	type readResult struct {
		line string
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		done <- readResult{line: line, err: err}
	}()
	select {
	case result := <-done:
		return result.line, result.err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", waitingFor)
		return "", nil
	}
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func assertProviderMessagesContainToolResult(t *testing.T, payload map[string]any, toolID string, toolName string, decision string) {
	t.Helper()
	messages, _ := payload["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("expected provider messages, got %#v", payload)
	}
	toolCallIndex := -1
	toolResultIndex := -1
	for index, raw := range messages {
		message, _ := raw.(map[string]any)
		if len(message) == 0 {
			continue
		}
		switch strings.TrimSpace(stringValue(message["role"])) {
		case "assistant":
			calls, _ := message["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				if strings.TrimSpace(stringValue(call["id"])) != toolID {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				if strings.TrimSpace(stringValue(fn["name"])) == toolName {
					toolCallIndex = index
				}
			}
		case "tool":
			if strings.TrimSpace(stringValue(message["tool_call_id"])) != toolID {
				continue
			}
			if strings.TrimSpace(stringValue(message["name"])) != toolName {
				t.Fatalf("tool result for %s used wrong tool name in payload %#v", toolID, message)
			}
			content := stringValue(message["content"])
			if !strings.Contains(content, `"decision":"`+decision+`"`) {
				t.Fatalf("tool result for %s missing decision %q in content %q", toolID, decision, content)
			}
			toolResultIndex = index
		}
	}
	if toolCallIndex < 0 || toolResultIndex < 0 || toolResultIndex < toolCallIndex {
		t.Fatalf("expected assistant tool_call before matching tool result id=%s name=%s, got messages %#v", toolID, toolName, messages)
	}
}

func assertFinalizePlanningToolVisibility(t *testing.T, body string, wantVisible bool) {
	t.Helper()
	hasFinalizePlanningTool := strings.Contains(body, `"toolName":"finalize_planning"`) || strings.Contains(body, `"toolId":"tool_plan"`)
	if wantVisible {
		if !hasFinalizePlanningTool || !strings.Contains(body, `"type":"tool.args"`) {
			t.Fatalf("expected visible finalize_planning tool events, got %s", body)
		}
		return
	}
	if hasFinalizePlanningTool {
		t.Fatalf("did not expect hidden finalize_planning tool events, got %s", body)
	}
}

func providerToolCallArgsDeltaFrame(t *testing.T, toolID string, toolName string, argsDelta string, finishReason string) string {
	t.Helper()
	function := map[string]any{}
	if toolName != "" {
		function["name"] = toolName
	}
	if argsDelta != "" {
		function["arguments"] = argsDelta
	}
	toolCall := map[string]any{
		"index":    0,
		"function": function,
	}
	if toolID != "" {
		toolCall["id"] = toolID
	}
	if toolName != "" {
		toolCall["type"] = "function"
	}
	choice := map[string]any{
		"delta": map[string]any{
			"tool_calls": []any{toolCall},
		},
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	frame, err := json.Marshal(map[string]any{
		"choices": []any{choice},
	})
	if err != nil {
		t.Fatalf("marshal provider tool call delta frame: %v", err)
	}
	return string(frame)
}

func writeProviderSSEFrame(t *testing.T, w io.Writer, frame string) {
	t.Helper()
	if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
		t.Fatalf("write sse frame: %v", err)
	}
}

func assertCoderPlanningToolSet(t *testing.T, got []string) {
	t.Helper()
	if len(got) != 8 {
		t.Fatalf("coder planning tools length=%d tools=%#v", len(got), got)
	}
	assertStringSliceContains(t, got, "file_read", "file_glob", "file_grep", "datetime", "regex", "vision_recognize", "ask_user_question", "finalize_planning")
	assertStringSliceExcludes(t, got, "bash", "file_write", "file_edit", "desktop_action", "desktop_cdp", "agent_invoke", "plan_add_tasks", "plan_get_tasks", "plan_update_task")
}

func awaitingQuestionText(payload map[string]any) string {
	questions, _ := payload["questions"].([]any)
	if len(questions) == 0 {
		return ""
	}
	first, _ := questions[0].(map[string]any)
	return strings.TrimSpace(stringValue(first["question"]))
}

func awaitingApprovalID(payload map[string]any) string {
	if plan, _ := payload["plan"].(map[string]any); len(plan) > 0 {
		return strings.TrimSpace(stringValue(plan["id"]))
	}
	approvals, _ := payload["approvals"].([]any)
	if len(approvals) == 0 {
		return ""
	}
	first, _ := approvals[0].(map[string]any)
	return strings.TrimSpace(stringValue(first["id"]))
}

func submitFrontendAnswer(t *testing.T, server http.Handler, runID string, awaitingID string, answer string) {
	t.Helper()
	body := `{"agentKey":"coder-app","runId":"` + runID + `","awaitingId":"` + awaitingID + `","params":[{"answer":"` + answer + `"}]}`
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

func submitFrontendDecision(t *testing.T, server http.Handler, runID string, awaitingID string, decision string) {
	submitFrontendDecisionWithReason(t, server, runID, awaitingID, decision, "")
}

func submitFrontendDecisionWithReason(t *testing.T, server http.Handler, runID string, awaitingID string, decision string, reason string) {
	t.Helper()
	item := map[string]any{"id": "confirm", "decision": decision}
	if reason != "" {
		item["reason"] = reason
	}
	body, err := json.Marshal(map[string]any{
		"agentKey":   "coder-app",
		"runId":      runID,
		"awaitingId": awaitingID,
		"params":     []map[string]any{item},
	})
	if err != nil {
		t.Fatalf("marshal submit decision: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBuffer(body))
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
			for _, ev := range chatResp.Data.Events {
				if ev.Type == "planning.delta" {
					t.Fatalf("did not expect persisted planning.delta in %#v", chatResp.Data.Events)
				}
			}
			if detailEventTypeCountForServerTest(chatResp.Data.Events, "planning.snapshot") != 1 {
				t.Fatalf("expected awaiting-plan replay to synthesize one planning.snapshot, got %#v", chatResp.Data.Events)
			}
			if !detailHasPlanAwaitingWithPlanningFileForServerTest(chatResp.Data.Events) {
				t.Fatalf("expected persisted plan awaiting to carry planningFile, got %#v", chatResp.Data.Events)
			}
			assertPlanningSnapshotBeforePlanAwaitingForServerTest(t, chatResp.Data.Events)
			planning, _ := chatResp.Data.Planning.(map[string]any)
			if len(planning) == 0 || strings.TrimSpace(anyString(planning["planningId"])) == "" ||
				strings.TrimSpace(anyString(planning["planningFile"])) == "" ||
				!strings.Contains(anyString(planning["text"]), "#") {
				t.Fatalf("expected persisted planning state from file, got %#v", chatResp.Data.Planning)
			}
			return
		}
	}
	t.Fatalf("expected persisted request.query planningMode=true, got %#v", chatResp.Data.Events)
}

func assertJSONLFinalizePlanningHistory(t *testing.T, store chat.Store, chatID string, decisions map[string]string) {
	t.Helper()
	content, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load chat jsonl: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	toolCalls := map[string]bool{}
	toolResults := map[string]string{}
	for {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode chat jsonl line: %v\n%s", err, content)
		}
		if stringValue(line["_type"]) == "planning" {
			t.Fatalf("did not expect persisted _type planning row, got:\n%s", content)
		}
		messages, _ := line["messages"].([]any)
		for _, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			switch stringValue(message["role"]) {
			case "assistant":
				for _, rawCall := range anySliceForServerTest(message["tool_calls"]) {
					call, _ := rawCall.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					if stringValue(fn["name"]) == "finalize_planning" {
						toolCalls[stringValue(call["id"])] = true
					}
				}
			case "tool":
				toolID := stringValue(message["tool_call_id"])
				if stringValue(message["name"]) == "finalize_planning" && toolID != "" {
					toolResults[toolID] = textFromJSONLMessageContentForServerTest(message["content"])
				}
			}
		}
	}
	for toolID, decision := range decisions {
		if !toolCalls[toolID] {
			t.Fatalf("expected JSONL assistant finalize_planning tool_call %s, got:\n%s", toolID, content)
		}
		result := toolResults[toolID]
		if !strings.Contains(result, `"decision":"`+decision+`"`) {
			t.Fatalf("expected JSONL tool result %s decision %q, got result %q in:\n%s", toolID, decision, result, content)
		}
	}
}

func assertJSONLCoderExecuteSyntheticQuery(t *testing.T, store chat.Store, chatID string, wantMessage string) {
	t.Helper()
	content, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load chat jsonl: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	var lines []map[string]any
	for {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode chat jsonl line: %v\n%s", err, content)
		}
		lines = append(lines, line)
	}
	for index, line := range lines {
		if stringValue(line["_type"]) != chat.StepLineTypeReactTool || !lineHasFinalizePlanningToolResultForServerTest(line) {
			continue
		}
		queryIndex := -1
		var queryLine map[string]any
		for scan := index + 1; scan < len(lines); scan++ {
			candidate := lines[scan]
			if stringValue(candidate["_type"]) != "query" {
				continue
			}
			query, _ := candidate["query"].(map[string]any)
			if query["synthetic"] == true && stringValue(query["stage"]) == "coder-execute" && stringValue(query["source"]) == "coder-plan-approve" {
				queryIndex = scan
				queryLine = candidate
				break
			}
		}
		if queryIndex < 0 {
			t.Fatalf("expected synthetic query after react-tool, got none in:\n%s", content)
		}
		query, _ := queryLine["query"].(map[string]any)
		if query["synthetic"] != true || stringValue(query["message"]) != wantMessage ||
			stringValue(query["stage"]) != "coder-execute" || stringValue(query["source"]) != "coder-plan-approve" {
			t.Fatalf("unexpected synthetic query payload %#v in:\n%s", query, content)
		}
		if _, ok := query["messages"]; ok {
			t.Fatalf("did not expect messages inside synthetic query payload %#v", query)
		}
		if _, ok := query["systems"]; ok {
			t.Fatalf("did not expect systems inside synthetic query payload %#v", query)
		}
		rawSystems, _ := queryLine["systems"].([]any)
		if len(rawSystems) != 1 {
			t.Fatalf("expected execute system on synthetic query, got %#v", queryLine)
		}
		systemKeys := map[string]bool{}
		for _, rawSystem := range rawSystems {
			system, _ := rawSystem.(map[string]any)
			systemKeys[stringValue(system["cacheKey"])] = true
		}
		if !systemKeys["coder:execute"] {
			t.Fatalf("expected coder execute system keys, got %#v in %#v", systemKeys, queryLine)
		}
		rawMessages, _ := queryLine["messages"].([]any)
		if len(rawMessages) != 1 {
			t.Fatalf("expected one synthetic query model message, got %#v in:\n%s", queryLine, content)
		}
		message, _ := rawMessages[0].(map[string]any)
		executePrompt := textFromJSONLMessageContentForServerTest(message["content"])
		if stringValue(message["role"]) != "user" ||
			!strings.Contains(executePrompt, "Execute the confirmed CODER plan.") ||
			!strings.Contains(executePrompt, "Original request:\nplease plan first") ||
			!strings.Contains(executePrompt, "Confirmed plan:\n# Confirm Coder Plan") {
			t.Fatalf("unexpected synthetic query model message %#v", message)
		}
		queryRunID := stringValue(queryLine["runId"])
		var executeLine map[string]any
		for scan := queryIndex + 1; scan < len(lines); scan++ {
			candidate := lines[scan]
			if stringValue(candidate["_type"]) != chat.StepLineTypeReact {
				continue
			}
			if queryRunID != "" && stringValue(candidate["runId"]) != queryRunID {
				continue
			}
			executeLine = candidate
			break
		}
		if executeLine == nil {
			t.Fatalf("expected execute react after synthetic query, got none in:\n%s", content)
		}
		if stringValue(executeLine["_type"]) != chat.StepLineTypeReact {
			t.Fatalf("expected execute react after synthetic query, got %#v in:\n%s", executeLine, content)
		}
		if _, ok := executeLine["inputMessages"]; ok {
			t.Fatalf("did not expect duplicate execute inputMessages on first execute react %#v", executeLine)
		}
		systemRef, _ := executeLine["systemRef"].(map[string]any)
		if systemRef["cacheKey"] != "coder:execute" {
			t.Fatalf("expected execute react to keep coder:execute systemRef, got %#v", executeLine)
		}
		if _, ok := executeLine["systems"]; ok {
			t.Fatalf("did not expect execute react systems, got %#v", executeLine)
		}
		if got := strings.Count(content, "Execute the confirmed CODER plan.\\n\\nOriginal request:"); got != 1 {
			t.Fatalf("expected execute prompt persisted once, got %d in:\n%s", got, content)
		}
		return
	}
	t.Fatalf("expected react-tool finalize_planning result in:\n%s", content)
}

func lineHasFinalizePlanningToolResultForServerTest(line map[string]any) bool {
	messages, _ := line["messages"].([]any)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		if stringValue(message["role"]) == "tool" &&
			stringValue(message["name"]) == contracts.FinalizePlanningToolName {
			return true
		}
	}
	return false
}

func anySliceForServerTest(value any) []any {
	items, _ := value.([]any)
	return items
}

func textFromJSONLMessageContentForServerTest(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var b strings.Builder
	for _, raw := range anySliceForServerTest(value) {
		part, _ := raw.(map[string]any)
		b.WriteString(stringValue(part["text"]))
	}
	return b.String()
}

func assertPlanningSnapshotBeforePlanAwaitingForServerTest(t *testing.T, events []stream.EventData) {
	t.Helper()
	snapshotIndex := -1
	awaitingIndex := -1
	var snapshot stream.EventData
	var awaiting stream.EventData
	for idx, event := range events {
		if event.Type == "planning.snapshot" && snapshotIndex < 0 {
			snapshotIndex = idx
			snapshot = event
		}
		if event.Type == "awaiting.ask" && strings.EqualFold(strings.TrimSpace(anyString(event.Value("mode"))), "plan") && awaitingIndex < 0 {
			awaitingIndex = idx
			awaiting = event
		}
	}
	if snapshotIndex < 0 || awaitingIndex < 0 || snapshotIndex >= awaitingIndex {
		t.Fatalf("expected planning.snapshot before plan awaiting.ask, got %#v", events)
	}
	plan, _ := awaiting.Value("plan").(map[string]any)
	if snapshot.String("planningId") != strings.TrimSpace(anyString(plan["planningId"])) ||
		snapshot.String("planningFile") != strings.TrimSpace(anyString(plan["planningFile"])) ||
		!strings.Contains(snapshot.String("text"), "#") {
		t.Fatalf("unexpected replay planning.snapshot=%#v awaiting=%#v", snapshot, awaiting)
	}
}

func detailHasPlanAwaitingWithPlanningFileForServerTest(events []stream.EventData) bool {
	for _, event := range events {
		if event.Type != "awaiting.ask" || !strings.EqualFold(strings.TrimSpace(anyString(event.Value("mode"))), "plan") {
			continue
		}
		plan, _ := event.Value("plan").(map[string]any)
		if strings.TrimSpace(anyString(plan["planningId"])) != "" && strings.TrimSpace(anyString(plan["planningFile"])) != "" {
			return true
		}
	}
	return false
}

func detailEventTypeCountForServerTest(events []stream.EventData, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func TestQueryStreamsToolPayloadEventsAndPersistsToolSnapshot(t *testing.T) {
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
			`{"choices":[{"delta":{"content":"payload visible"}}]}`,
			`{"choices":[{"delta":{"content":" from sse"},"finish_reason":"stop"}]}`,
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
	if strings.Contains(body, `.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.start"`) || !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool lifecycle to remain in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.args"`) || !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected live stream to include tool payload events, got %s", body)
	}

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
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
	fixture.server.ServeHTTP(interruptRec, httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`"}`)))
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
