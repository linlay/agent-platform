package server

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/engine"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/stream"
)

var disallowedPersistedEventTypes = []string{
	"reasoning.start",
	"reasoning.delta",
	"reasoning.end",
	"content.start",
	"content.delta",
	"content.end",
	"tool.start",
	"tool.args",
	"tool.end",
	"action.start",
	"action.args",
	"action.end",
}

func TestStatusRecorderExposesFlusherWhenUnderlyingWriterSupportsIt(t *testing.T) {
	base := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	flusher, ok := any(rec).(http.Flusher)
	if !ok {
		t.Fatalf("expected statusRecorder to implement http.Flusher")
	}

	flusher.Flush()
	if !base.Flushed {
		t.Fatalf("expected Flush to be forwarded to underlying response writer")
	}
}

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

func TestUploadAndResourceRoundTrip(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello world")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("requestId", "req_upload"); err != nil {
		t.Fatalf("write requestId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	assertUUIDLike(t, response.Data.ChatID)
	resourceReq := httptest.NewRequest(http.MethodGet, response.Data.Upload.URL, nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 resource, got %d", resourceRec.Code)
	}
	if got := resourceRec.Body.String(); got != "hello world" {
		t.Fatalf("unexpected resource content: %q", got)
	}

	matches, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*", "notes.txt"))
	if err != nil {
		t.Fatalf("glob upload path: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected uploaded file under %s, got %v", fixture.cfg.Paths.ChatsDir, matches)
	}
}

func TestRememberEndpointReturnsStoredMemory(t *testing.T) {
	fixture := newTestFixture(t)
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
	if len(chatResp.Data.Events) != 11 {
		t.Fatalf("expected 11 persisted events for two turns (including react step markers), got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
	}
	if len(chatResp.Data.RawMessages) != 4 {
		t.Fatalf("expected four raw messages for two turns, got %#v", chatResp.Data.RawMessages)
	}
}

func TestServeHTTPLogsArrivalBeforeCompletion(t *testing.T) {
	fixture := newTestFixture(t)

	var buffer bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(originalWriter)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	logText := buffer.String()
	arrived := strings.Index(logText, "GET /api/agents (arrived)")
	completed := strings.Index(logText, "GET /api/agents -> 200")
	if arrived < 0 {
		t.Fatalf("expected arrival log, got %q", logText)
	}
	if completed < 0 {
		t.Fatalf("expected completion log, got %q", logText)
	}
	if arrived > completed {
		t.Fatalf("expected arrival log before completion log, got %q", logText)
	}
}

func TestAgentEndpointReturnsDetail(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=mock-runner", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if response.Data.Key != "mock-runner" {
		t.Fatalf("expected mock-runner key, got %#v", response.Data)
	}
	if response.Data.Model != "mock-model-id" {
		t.Fatalf("expected resolved model id, got %#v", response.Data)
	}
	if response.Data.Mode != "REACT" {
		t.Fatalf("expected REACT mode, got %#v", response.Data)
	}
	if len(response.Data.Tools) != 3 || response.Data.Tools[0] != "_datetime_" || response.Data.Tools[2] != "_sandbox_bash_" {
		t.Fatalf("expected tools in detail response, got %#v", response.Data.Tools)
	}
	if len(response.Data.Skills) != 1 || response.Data.Skills[0] != "mock-skill" {
		t.Fatalf("expected skills in detail response, got %#v", response.Data.Skills)
	}
	if len(response.Data.Controls) != 1 || response.Data.Controls[0]["key"] != "tone" {
		t.Fatalf("expected controls in detail response, got %#v", response.Data.Controls)
	}
	if response.Data.Meta["modelKey"] != "mock-model" {
		t.Fatalf("expected modelKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["providerKey"] != "mock" {
		t.Fatalf("expected providerKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["protocol"] != "OPENAI" {
		t.Fatalf("expected protocol meta, got %#v", response.Data.Meta)
	}
	modelKeys, ok := response.Data.Meta["modelKeys"].([]any)
	if !ok || len(modelKeys) != 1 || modelKeys[0] != "mock-model" {
		t.Fatalf("expected modelKeys meta, got %#v", response.Data.Meta["modelKeys"])
	}
	perAgentSkills, ok := response.Data.Meta["perAgentSkills"].([]any)
	if !ok || len(perAgentSkills) != 1 || perAgentSkills[0] != "mock-skill" {
		t.Fatalf("expected perAgentSkills meta, got %#v", response.Data.Meta["perAgentSkills"])
	}
	sandbox, ok := response.Data.Meta["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox meta, got %#v", response.Data.Meta)
	}
	if sandbox["level"] != "RUN" {
		t.Fatalf("expected sandbox level RUN, got %#v", sandbox["level"])
	}
	extraMounts, ok := sandbox["extraMounts"].([]any)
	if !ok || len(extraMounts) != 1 {
		t.Fatalf("expected sandbox extraMounts, got %#v", sandbox)
	}
	firstMount, ok := extraMounts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first sandbox mount map, got %#v", extraMounts[0])
	}
	if _, exists := firstMount["source"]; !exists || firstMount["source"] != nil {
		t.Fatalf("expected sandbox mount source=null, got %#v", firstMount)
	}
	if firstMount["destination"] != "/skills" {
		t.Fatalf("expected sandbox mount destination /skills, got %#v", firstMount)
	}
}

func TestAgentEndpointRequiresAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEndpointRejectsBlankAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=%20%20%20", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEndpointReturnsNotFoundForUnknownAgent(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=missing-agent", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogEndpoints(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	for _, path := range []string{"/api/agents", "/api/agent?agentKey=mock-runner", "/api/teams", "/api/skills", "/api/tools", "/api/tool?toolName=_bash_"} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", path, rec.Code)
		}
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
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"_datetime_","arguments":"{"}}]}}]}`,
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

func TestQueryPersistsToolSnapshotWhenSSEPayloadEventsDisabled(t *testing.T) {
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
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"_datetime_","arguments":"{"}}]}}]}`,
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
	fixture.cfg.SSE.IncludeToolPayloadEvents = false
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
	if !strings.Contains(body, `"type":"tool.start"`) || !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool lifecycle to remain in sse, got %s", body)
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
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"_datetime_","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
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
	httpServer := httptest.NewServer(fixture.server)
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

	httpServer := httptest.NewServer(fixture.server)
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

func TestFrontendSubmitAndSteerAreConsumedBeforeNextTurn(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_confirm","type":"function","function":{"name":"confirm_dialog","arguments":"{\"title\":\"Need confirmation\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please confirm first"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "request.submit" {
				runID, _ = payload["runId"].(string)
				toolID, _ = payload["toolId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"runId":"`+runID+`","message":"Please keep it short."}`))
	steerReq.Header.Set("Content-Type", "application/json")
	steerRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(steerRec, steerReq)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("steer expected 200, got %d: %s", steerRec.Code, steerRec.Body.String())
	}
	var steerResp api.ApiResponse[api.SteerResponse]
	if err := json.Unmarshal(steerRec.Body.Bytes(), &steerResp); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if !steerResp.Data.Accepted || steerResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted steer, got %#v", steerResp.Data)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","toolId":"`+toolID+`","params":{"confirmed":true}}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	var submitResp api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(submitRec.Body.Bytes(), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !submitResp.Data.Accepted || submitResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted submit, got %#v", submitResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.steer"`) {
		t.Fatalf("expected request.steer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected final answer in stream, got %s", body)
	}

	select {
	case messages := <-secondTurnMessages:
		toolIndex := -1
		steerIndex := -1
		for i, message := range messages {
			role, _ := message["role"].(string)
			content, _ := message["content"].(string)
			if role == "tool" {
				toolIndex = i
			}
			if role == "user" && content == "Please keep it short." {
				steerIndex = i
			}
		}
		if toolIndex < 0 {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if steerIndex <= toolIndex {
			t.Fatalf("expected steer message after tool message, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestSubmitReturnsUnmatchedWhenNoActiveWaiter(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"missing-run","toolId":"missing-tool","params":{"ok":true}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if response.Data.Accepted {
		t.Fatalf("expected unmatched submit to be rejected, got %#v", response.Data)
	}
	if response.Data.Status != "unmatched" {
		t.Fatalf("expected unmatched status, got %#v", response.Data)
	}
}

func TestServerRejectsInvalidLocalJWTConfigAtStartup(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: filepath.Join(fixture.cfg.Paths.ChatsDir, "missing.pem"),
	}

	_, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err == nil {
		t.Fatal("expected startup auth config error")
	}
	if !strings.Contains(err.Error(), "load local jwt public key") {
		t.Fatalf("expected local key error, got %v", err)
	}
}

func TestQueryAcceptsValidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
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

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "zenmind-local",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", rec.Body.String())
	}
}

func TestQueryRejectsInvalidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
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

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "wrong-issuer",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

type testFixture struct {
	server          *Server
	cfg             config.Config
	chats           chat.Store
	memories        memory.Store
	registry        catalog.Registry
	runs            engine.RunManager
	agent           engine.AgentEngine
	tools           engine.ToolExecutor
	sandbox         engine.SandboxClient
	mcp             engine.McpClient
	viewport        engine.ViewportClient
	catalogReloader engine.CatalogReloader
}

func newTestFixture(t *testing.T) testFixture {
	return newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runner "}}]}`,
			`{"choices":[{"delta":{"content":"test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
}

func newTestFixtureWithModelHandler(t *testing.T, modelHandler http.HandlerFunc) testFixture {
	t.Helper()
	root := t.TempDir()
	providerServer := httptest.NewServer(modelHandler)
	t.Cleanup(providerServer.Close)

	registriesDir := filepath.Join(root, "registries")
	agentsDir := filepath.Join(root, "agents")
	teamsDir := filepath.Join(root, "teams")
	skillsDir := filepath.Join(root, "skills-market")
	providersDir := filepath.Join(registriesDir, "providers")
	modelsDir := filepath.Join(registriesDir, "models")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, "mock-runner"), 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatalf("mkdir teams dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "mock-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + providerServer.URL,
		"apiKey: test-key",
		"defaultModel: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"isFunction: true",
		"isReasoner: false",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "mock-runner", "agent.yml"), []byte(strings.Join([]string{
		"key: mock-runner",
		"name: Mock Runner",
		"role: 测试代理",
		"description: test agent",
		"modelConfig:",
		"  modelKey: mock-model",
		"toolConfig:",
		"  backends:",
		"    - _datetime_",
		"  frontends:",
		"    - confirm_dialog",
		"  actions: []",
		"skillConfig:",
		"  skills:",
		"    - mock-skill",
		"controls:",
		"  - key: tone",
		"    type: select",
		"    label: 输出语气",
		"    defaultValue: concise",
		"    options:",
		"      - value: concise",
		"        label: 简洁",
		"sandboxConfig:",
		"  environmentId: shell",
		"  level: RUN",
		"  extraMounts:",
		"    - platform: skills-market",
		"      destination: /skills",
		"      mode: ro",
		"mode: REACT",
		"react:",
		"  maxSteps: 6",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamsDir, "default.demo.yml"), []byte(strings.Join([]string{
		"name: Default Team",
		"defaultAgentKey: mock-runner",
		"agentKeys:",
		"  - mock-runner",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write team config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "mock-skill", "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill config: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Port: "18080",
		},
		Paths: config.PathsConfig{
			RegistriesDir:   registriesDir,
			AgentsDir:       agentsDir,
			TeamsDir:        teamsDir,
			SkillsMarketDir: skillsDir,
			ChatsDir:        filepath.Join(root, "custom-chats"),
			MemoryDir:       filepath.Join(root, "custom-memory"),
		},
		Auth: config.AuthConfig{
			Enabled: false,
		},
		ChatImage: config.ChatImageTokenConfig{
			ResourceTicketEnabled: false,
		},
		SSE: config.SSEConfig{
			IncludeToolPayloadEvents: true,
		},
		Defaults: config.DefaultsConfig{
			React: config.ReactDefaultsConfig{MaxSteps: 6},
		},
		Logging: config.LoggingConfig{
			Request: config.ToggleConfig{Enabled: true},
		},
		Skills: config.SkillCatalogConfig{
			CatalogConfig:  config.CatalogConfig{ExternalDir: skillsDir},
			MaxPromptChars: 8000,
		},
		Bash: config.BashConfig{
			WorkingDirectory:        root,
			AllowedPaths:            []string{root, "/tmp"},
			AllowedCommands:         []string{"pwd", "echo", "ls", "cat"},
			PathCheckedCommands:     []string{"ls", "cat"},
			PathCheckBypassCommands: []string{},
			ShellExecutable:         "bash",
			ShellTimeoutMs:          30000,
			MaxCommandChars:         16000,
		},
		ContainerHub: config.ContainerHubConfig{
			Enabled: false,
		},
	}

	chats, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(cfg.Paths.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	modelRegistry, err := engine.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	backendTools := engine.NewRuntimeToolExecutor(cfg, engine.NewNoopSandboxClient(), memories)
	mcp := engine.NewNoopMcpClient()
	toolExecutor := engine.NewToolRouter(backendTools, mcp, engine.NewFrontendSubmitCoordinator(), engine.NewNoopActionInvoker())
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	reloader := engine.NewRuntimeCatalogReloader(registry, modelRegistry)

	runs := engine.NewInMemoryRunManager()
	sandbox := engine.NewNoopSandboxClient()
	agentEngine := engine.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, sandbox)
	viewport := engine.NewNoopViewportClient()
	server, err := New(Dependencies{
		Config:          cfg,
		Chats:           chats,
		Memory:          memories,
		Registry:        registry,
		Models:          modelRegistry,
		Runs:            runs,
		Agent:           agentEngine,
		Tools:           toolExecutor,
		Sandbox:         sandbox,
		MCP:             mcp,
		Viewport:        viewport,
		CatalogReloader: reloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	return testFixture{
		server:          server,
		cfg:             cfg,
		chats:           chats,
		memories:        memories,
		registry:        registry,
		runs:            runs,
		agent:           agentEngine,
		tools:           toolExecutor,
		sandbox:         sandbox,
		mcp:             mcp,
		viewport:        viewport,
		catalogReloader: reloader,
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

func writeProviderSSE(t *testing.T, w http.ResponseWriter, frames ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("expected flusher")
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
			t.Fatalf("write sse frame: %v", err)
		}
		flusher.Flush()
	}
}

func assertPersistedEventTypes(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	seen := make(map[string]int)
	for _, event := range events {
		eventType := event.Type
		seen[eventType]++
	}
	for _, eventType := range want {
		if seen[eventType] == 0 {
			t.Fatalf("expected persisted event type %q, got %#v", eventType, events)
		}
	}
	for _, eventType := range disallowedPersistedEventTypes {
		if seen[eventType] > 0 {
			t.Fatalf("did not expect persisted event type %q, got %#v", eventType, events)
		}
	}
}

type scriptedRoundTripper struct {
	handler http.HandlerFunc
}

func (r scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.handler(rec, req)
	result := rec.Result()
	return &http.Response{
		StatusCode: result.StatusCode,
		Status:     result.Status,
		Header:     result.Header.Clone(),
		Body:       result.Body,
		Request:    req,
	}, nil
}

func newScriptedHTTPClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{Transport: scriptedRoundTripper{handler: handler}}
}

func writeTestJWTKeyPair(t *testing.T, dir string) (*rsa.PrivateKey, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	path := filepath.Join(dir, "test-public-key.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(path, block, 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	return privateKey, path
}

func mustSignRS256JWT(t *testing.T, privateKey *rsa.PrivateKey, payload map[string]any) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func decodeSSEMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	lines := strings.Split(body, "\n")
	messages := make([]map[string]any, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		var msg map[string]any
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			t.Fatalf("decode sse message %q: %v", payload, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func decodeSSEPayloadStrings(body string) []string {
	lines := strings.Split(body, "\n")
	payloads := make([]string, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
	}
	return payloads
}

func decodeSSELine(t *testing.T, line string) map[string]any {
	t.Helper()
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	var message map[string]any
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode sse line %q: %v", line, err)
	}
	return message
}

func normalizeProviderMessages(value any) []map[string]any {
	items, _ := value.([]any)
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		message, _ := item.(map[string]any)
		messages = append(messages, message)
	}
	return messages
}

func assertSSEMessagesHaveSeqAndTimestamp(t *testing.T, body string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got body %s", body)
	}
	prevSeq := 0.0
	for _, msg := range messages {
		seq, ok := msg["seq"].(float64)
		if !ok || seq <= prevSeq {
			t.Fatalf("expected ascending seq, got %#v", messages)
		}
		prevSeq = seq
		if _, ok := msg["type"].(string); !ok {
			t.Fatalf("expected type field, got %#v", msg)
		}
		if ts, ok := msg["timestamp"].(float64); !ok || ts <= 0 {
			t.Fatalf("expected positive timestamp, got %#v", msg)
		}
	}
}

func assertSSEEventOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) < len(want) {
		t.Fatalf("expected at least %d messages, got %#v", len(want), messages)
	}
	for idx, eventType := range want {
		if messages[idx]["type"] != eventType {
			t.Fatalf("event %d: expected %s, got %#v", idx, eventType, messages[idx])
		}
	}
}

func assertSSEPayloadOrder(t *testing.T, body string, eventType string, parts []string) {
	t.Helper()
	for _, payload := range decodeSSEPayloadStrings(body) {
		if !strings.Contains(payload, `"type":"`+eventType+`"`) {
			continue
		}
		assertOrderedSubstrings(t, payload, parts)
		return
	}
	t.Fatalf("expected sse event type %s in body %s", eventType, body)
}

func assertBodyContainsOrderedEvent(t *testing.T, body string, marker string, parts []string) {
	t.Helper()
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("expected marker %q in body %s", marker, body)
	}
	start := strings.LastIndex(body[:index], "{")
	end := strings.Index(body[index:], "}")
	if start < 0 || end < 0 {
		t.Fatalf("expected json object around marker %q in body %s", marker, body)
	}
	assertOrderedSubstrings(t, body[start:index+end+1], parts)
}

func assertOrderedSubstrings(t *testing.T, body string, parts []string) {
	t.Helper()
	prev := -1
	for _, part := range parts {
		idx := strings.Index(body, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, body)
		}
		if idx <= prev {
			t.Fatalf("expected ordered substrings %v in %s", parts, body)
		}
		prev = idx
	}
}

func assertUUIDLike(t *testing.T, value string) {
	t.Helper()
	parts := strings.Split(value, "-")
	if len(parts) != 5 {
		t.Fatalf("expected uuid-like value, got %q", value)
	}
	lengths := []int{8, 4, 4, 4, 12}
	for idx, part := range parts {
		if len(part) != lengths[idx] {
			t.Fatalf("expected uuid-like value, got %q", value)
		}
	}
}
