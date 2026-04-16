package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
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
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/hitl"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/reload"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/stream"
	"agent-platform-runner-go/internal/tools"
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
		t.Fatalf("expected 11 persisted events for two turns, got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
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
	if len(response.Data.Tools) != 4 ||
		response.Data.Tools[0] != "_datetime_" ||
		response.Data.Tools[1] != "_ask_user_question_" ||
		response.Data.Tools[2] != "_ask_user_approval_" ||
		response.Data.Tools[3] != "_sandbox_bash_" {
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

func TestToolEndpointReturnsCanonicalJavaBuiltinSchemas(t *testing.T) {
	fixture := newTestFixture(t)

	for _, tc := range []struct {
		toolName         string
		requiredProperty string
	}{
		{toolName: "_memory_read_", requiredProperty: "sort"},
		{toolName: "_datetime_", requiredProperty: "timezone"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tool?toolName="+tc.toolName, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", tc.toolName, rec.Code, rec.Body.String())
		}

		var response api.ApiResponse[api.ToolDetailResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode tool response for %s: %v", tc.toolName, err)
		}
		if response.Data.Name != tc.toolName {
			t.Fatalf("expected tool %s, got %#v", tc.toolName, response.Data)
		}
		properties, _ := response.Data.Parameters["properties"].(map[string]any)
		if _, ok := properties[tc.requiredProperty]; !ok {
			t.Fatalf("expected property %s in %s schema, got %#v", tc.requiredProperty, tc.toolName, response.Data.Parameters)
		}
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
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_confirm","type":"function","function":{"name":"_ask_user_approval_","arguments":"{\"mode\":\"approval\",\"question\":\"Need confirmation\",\"options\":[{\"label\":\"Approve\",\"value\":\"approve\",\"description\":\"Continue with the request\"}],\"allowFreeText\":true,\"freeTextPlaceholder\":\"Type your own answer\"}"}}]},"finish_reason":"tool_calls"}]}`,
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
	var toolStartPayload map[string]any
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.start" && payload["toolName"] == "_ask_user_approval_" {
				toolStartPayload = payload
				toolID, _ = payload["toolId"].(string)
			}
			if payload["type"] == "awaiting.ask" {
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if toolStartPayload == nil {
		t.Fatalf("expected frontend tool.start before awaiting.ask, got %s", streamBody.String())
	}
	if _, exists := toolStartPayload["toolType"]; exists {
		t.Fatalf("did not expect toolType on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["viewportKey"]; exists {
		t.Fatalf("did not expect viewportKey on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on tool.start, got %#v", toolStartPayload)
	}
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask before submit, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["viewportType"] != "builtin" {
		t.Fatalf("expected viewportType builtin, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["viewportKey"] != "confirm_dialog" {
		t.Fatalf("expected viewportKey confirm_dialog, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["toolTimeout"] != float64(210000) {
		t.Fatalf("expected await question toolTimeout 210000, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "approval" {
		t.Fatalf("expected await question mode approval, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	approvalQuestions, _ := awaitQuestionPayload["questions"].([]any)
	if len(approvalQuestions) != 1 {
		t.Fatalf("expected approval awaiting.ask questions length 1, got %#v", awaitQuestionPayload)
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

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":{"value":"approve"}}`))
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
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event for approval mode, got %s", body)
	}
	if !strings.Contains(body, `"questions":[`) {
		t.Fatalf("expected top-level questions in approval awaiting.ask event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":{"value":"approve"}`) {
		t.Fatalf("expected request.submit params, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.steer"`) {
		t.Fatalf("expected request.steer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if !strings.Contains(body, `"mode":"approval"`) || !strings.Contains(body, `"value":"approve"`) {
		t.Fatalf("expected normalized approval tool.result, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected final answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "tool.start", "tool.end", "awaiting.ask", "request.submit", "awaiting.answer", "tool.result")

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
	foundFrontendSnapshot := false
	foundAwaitAsk := false
	foundRequestSubmit := false
	foundAwaitingAnswer := false
	for _, event := range chatResp.Data.Events {
		switch event.Type {
		case "tool.snapshot":
			if event.String("toolName") != "_ask_user_approval_" {
				continue
			}
			foundFrontendSnapshot = true
			if _, exists := event.Payload["toolType"]; exists {
				t.Fatalf("did not expect frontend snapshot toolType, got %#v", event)
			}
			if _, exists := event.Payload["viewportKey"]; exists {
				t.Fatalf("did not expect frontend snapshot viewportKey, got %#v", event)
			}
			if _, exists := event.Payload["toolTimeout"]; exists {
				t.Fatalf("did not expect frontend snapshot toolTimeout, got %#v", event)
			}
		case "awaiting.ask":
			foundAwaitAsk = true
			if event.String("viewportKey") != "confirm_dialog" {
				t.Fatalf("unexpected awaiting.ask payload %#v", event)
			}
			if _, exists := event.Payload["awaitName"]; exists {
				t.Fatalf("did not expect awaitName on awaiting.ask in chat detail, got %#v", event)
			}
			if _, exists := event.Payload["chatId"]; exists {
				t.Fatalf("did not expect chatId on awaiting.ask in chat detail, got %#v", event)
			}
			questions, _ := event.Payload["questions"].([]any)
			if len(questions) != 1 {
				t.Fatalf("expected approval awaiting.ask questions length 1, got %#v", event)
			}
		case "request.submit":
			foundRequestSubmit = true
			if event.Value("params") == nil {
				t.Fatalf("expected params on request.submit in chat detail, got %#v", event)
			}
		case "awaiting.answer":
			foundAwaitingAnswer = true
			if event.String("mode") != "approval" || event.String("value") != "approve" {
				t.Fatalf("unexpected awaiting.answer in chat detail %#v", event)
			}
		}
	}
	if !foundFrontendSnapshot {
		t.Fatalf("expected _ask_user_approval_ tool.snapshot in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitAsk {
		t.Fatalf("expected awaiting.ask in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundRequestSubmit {
		t.Fatalf("expected request.submit in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitingAnswer {
		t.Fatalf("expected awaiting.answer in chat detail, got %#v", chatResp.Data.Events)
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

func TestQuestionAwaitFollowsToolStartAndPrecedesToolArgs(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Notification topics\",\"type\":\"select\",\"options\":[{\"label\":\"产品更新\",\"description\":\"Release notes and new features\"},{\"label\":\"使用教程\",\"description\":\"How-to guides and walkthroughs\"}],\"allowFreeText\":false,\"multiSelect\":true},{\"question\":\"How many people?\",\"type\":\"number\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var awaitQuestionPayload map[string]any
	var toolStartPayload map[string]any
	var toolResultPayload map[string]any
	var awaitPayloadSeen bool
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
			case "tool.start":
				if payload["toolName"] == "_ask_user_question_" {
					toolStartPayload = payload
					toolID, _ = payload["toolId"].(string)
				}
			case "awaiting.payload":
				questions, _ := payload["questions"].([]any)
				if len(questions) != 2 {
					t.Fatalf("expected question awaiting.payload questions length 2, got %#v", payload)
				}
				awaitPayloadSeen = true
				break
			}
			if awaitPayloadSeen {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask after tool.start and before tool.args, got %s", streamBody.String())
	}
	if toolStartPayload == nil {
		t.Fatalf("expected tool.start for _ask_user_question_, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["questions"]; exists {
		t.Fatalf("did not expect questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if !awaitPayloadSeen {
		t.Fatalf("expected awaiting.payload before submit, got %s", streamBody.String())
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"question":"Notification topics","answers":["产品更新","使用教程"]},{"question":"How many people?","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("expected awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"questions":[`) {
		t.Fatalf("expected top-level questions in awaiting.payload event, got %s", body)
	}
	if strings.Contains(body, `"payload":{"mode":"question"`) {
		t.Fatalf("did not expect nested payload mode in question awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":[{"answers":["产品更新","使用教程"],"question":"Notification topics"},{"answer":2,"question":"How many people?"}]`) {
		t.Fatalf("expected request.submit params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"answers":[{"answer":"产品更新, 使用教程","question":"Notification topics"},{"answer":"2","question":"How many people?"}]`) {
		t.Fatalf("expected formatted awaiting.answer answers, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if strings.Contains(body, `"result":{"mode":"question"`) {
		t.Fatalf("did not expect normalized question wrapper in tool.result, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	resultItems, ok := toolResultPayload["result"].([]any)
	if !ok || len(resultItems) != 2 {
		t.Fatalf("expected raw submit array in tool.result, got %#v", toolResultPayload)
	}
	firstItem, _ := resultItems[0].(map[string]any)
	secondItem, _ := resultItems[1].(map[string]any)
	firstAnswers, _ := firstItem["answers"].([]any)
	if firstItem["question"] != "Notification topics" || len(firstAnswers) != 2 || firstAnswers[0] != "产品更新" || firstAnswers[1] != "使用教程" {
		t.Fatalf("unexpected first tool.result item: %#v", firstItem)
	}
	if secondItem["question"] != "How many people?" || secondItem["answer"] != float64(2) {
		t.Fatalf("unexpected second tool.result item: %#v", secondItem)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "awaiting.payload", "request.submit", "awaiting.answer", "tool.result")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if toolContent != "问题：Notification topics\n回答：产品更新, 使用教程\n问题：How many people?\n回答：2" {
			t.Fatalf("expected qa-formatted tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionAwaitDismissReturnsCancelledStructuredResult(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick a plan\",\"type\":\"select\",\"options\":[{\"label\":\"Weekend\",\"description\":\"2 days\"}],\"allowFreeText\":false,\"multiSelect\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question cancel flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	awaitPayloadSeen := false
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				runID, _ = payload["runId"].(string)
				toolID, _ = payload["awaitingId"].(string)
			case "awaiting.payload":
				awaitPayloadSeen = true
			}
			if awaitPayloadSeen {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.submit"`) || !strings.Contains(body, `"params":[]`) {
		t.Fatalf("expected request.submit with empty params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) || !strings.Contains(body, `"cancelled":true`) || !strings.Contains(body, `"reason":"user_dismissed"`) {
		t.Fatalf("expected cancelled awaiting.answer in stream, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	resultMap, ok := toolResultPayload["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected structured tool.result object, got %#v", toolResultPayload)
	}
	if resultMap["mode"] != "question" || resultMap["cancelled"] != true || resultMap["reason"] != "user_dismissed" {
		t.Fatalf("unexpected cancelled tool.result payload %#v", resultMap)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "awaiting.payload", "request.submit", "awaiting.answer", "tool.result")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if !strings.Contains(toolContent, `"cancelled":true`) || !strings.Contains(toolContent, `"mode":"question"`) || !strings.Contains(toolContent, `"reason":"user_dismissed"`) {
			t.Fatalf("expected cancelled JSON tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

type recordingSandbox struct {
	commands []string
}

func (s *recordingSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *recordingSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64) (contracts.SandboxExecutionResult, error) {
	s.commands = append(s.commands, command)
	return contracts.SandboxExecutionResult{
		ExitCode:         0,
		Stdout:           "executed: " + command,
		Stderr:           "",
		WorkingDirectory: cwd,
	}, nil
}

func (s *recordingSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func TestBashHITLApproveFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, "approve", "")
	if len(executed) != 1 || executed[0] != "git push origin main" {
		t.Fatalf("expected approved command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"_hitl_confirm_dialog_"`) {
		t.Fatalf("expected synthetic HITL tool in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) || !strings.Contains(body, `"value":"approve"`) {
		t.Fatalf("expected approve awaiting.answer in stream, got %s", body)
	}
}

func TestBashHITLModifyFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, "modify", "git push origin release")
	if len(executed) != 1 || executed[0] != "git push origin release" {
		t.Fatalf("expected modified command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"action":"modify"`) {
		t.Fatalf("expected modify synthetic result, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) || !strings.Contains(body, `"value":"modify"`) || !strings.Contains(body, `"freeText":"git push origin release"`) {
		t.Fatalf("expected modify awaiting.answer in stream, got %s", body)
	}
}

func TestBashHITLRejectFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, "reject", "")
	if len(executed) != 0 {
		t.Fatalf("expected rejected command not to execute, got %#v", executed)
	}
	if !strings.Contains(body, `"code":"hitl_rejected"`) {
		t.Fatalf("expected rejected original bash result, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) || !strings.Contains(body, `"value":"reject"`) {
		t.Fatalf("expected reject awaiting.answer in stream, got %s", body)
	}
}

func runBashHITLFlow(t *testing.T, action string, modifiedCommand string) (string, []string) {
	t.Helper()

	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)
	sandbox := &recordingSandbox{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_bash","type":"function","function":{"name":"_sandbox_bash_","arguments":"{\"command\":\"git push origin main\",\"cwd\":\"/workspace\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"bash hitl complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox: sandbox,
		configure: func(cfg *config.Config) {
			cfg.BashHITL.Enabled = true
			cfg.BashHITL.DefaultTimeoutMs = 15000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			root := filepath.Join(cfg.Paths.RegistriesDir, "bash-hitl")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir bash-hitl dir: %v", err)
			}
			content := strings.Join([]string{
				"commands:",
				"  - command: git",
				"    subcommands:",
				"      - match: push",
				"        level: 2",
				"        hitlType: system",
				"        viewportType: builtin",
				"        viewportKey: confirm_dialog",
			}, "\n")
			if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(content), 0o644); err != nil {
				t.Fatalf("write bash-hitl rule: %v", err)
			}
		},
	})

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	originalToolID := ""
	syntheticToolID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "tool.start":
				switch payload["toolName"] {
				case "_sandbox_bash_":
					originalToolID, _ = payload["toolId"].(string)
				case "_hitl_confirm_dialog_":
					syntheticToolID, _ = payload["toolId"].(string)
				}
			case "awaiting.ask":
				if syntheticToolID == "" {
					syntheticToolID, _ = payload["awaitingId"].(string)
				}
				goto submit
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

submit:
	submitPayload := `{"action":"` + action + `"}`
	if action == "modify" {
		submitPayload = `{"action":"modify","command":"` + modifiedCommand + `"}`
	}
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+syntheticToolID+`","params":`+submitPayload+`}`)))
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
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

	messages := decodeSSEMessages(t, streamBody.String())
	assertSpecificEventOrder(t, messages, originalToolID, syntheticToolID)
	select {
	case secondTurn := <-secondTurnMessages:
		toolMessages := 0
		for _, message := range secondTurn {
			role, _ := message["role"].(string)
			if role == "tool" {
				toolMessages++
			}
		}
		if toolMessages < 2 {
			t.Fatalf("expected second turn to receive both tool messages, got %#v", secondTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}

	return streamBody.String(), append([]string(nil), sandbox.commands...)
}

func extractRunIDFromStream(t *testing.T, body string) string {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if runID, _ := message["runId"].(string); runID != "" {
			return runID
		}
	}
	t.Fatalf("expected runId in stream body: %s", body)
	return ""
}

func assertSpecificEventOrder(t *testing.T, messages []map[string]any, originalToolID string, syntheticToolID string) {
	t.Helper()
	originalStart := -1
	syntheticStart := -1
	awaitAsk := -1
	requestSubmit := -1
	awaitingAnswer := -1
	syntheticResult := -1
	originalResult := -1
	for idx, message := range messages {
		eventType, _ := message["type"].(string)
		switch eventType {
		case "tool.start":
			switch message["toolId"] {
			case originalToolID:
				originalStart = idx
			case syntheticToolID:
				syntheticStart = idx
			}
		case "awaiting.ask":
			if message["awaitingId"] == syntheticToolID {
				awaitAsk = idx
			}
		case "request.submit":
			if message["awaitingId"] == syntheticToolID {
				requestSubmit = idx
			}
		case "awaiting.answer":
			if message["awaitingId"] == syntheticToolID {
				awaitingAnswer = idx
			}
		case "tool.result":
			if message["toolId"] == syntheticToolID {
				syntheticResult = idx
			}
			if message["toolId"] == originalToolID {
				originalResult = idx
			}
		}
	}
	if !(originalStart >= 0 && syntheticStart > originalStart && awaitAsk > syntheticStart && requestSubmit > awaitAsk && awaitingAnswer > requestSubmit && syntheticResult > awaitingAnswer && originalResult > syntheticResult) {
		t.Fatalf("unexpected HITL event order: %#v", messages)
	}
}

func TestSubmitReturnsUnmatchedWhenNoActiveWaiter(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"missing-run","awaitingId":"missing-awaiting","params":{"ok":true}}`))
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

func assertEventOrder(t *testing.T, body string, eventTypes ...string) {
	t.Helper()
	prev := -1
	for _, eventType := range eventTypes {
		needle := `"type":"` + eventType + `"`
		index := strings.Index(body, needle)
		if index < 0 {
			t.Fatalf("expected event %s in stream body: %s", eventType, body)
		}
		if index <= prev {
			t.Fatalf("expected event order %v in stream body: %s", eventTypes, body)
		}
		prev = index
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

func TestQueryRejectsMissingBearerWhenLocalJWTEnabled(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestExecuteInternalQueryBypassesHTTPAuth(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	status, body, err := server.ExecuteInternalQuery(context.Background(), api.QueryRequest{
		Message:  "计划任务内部执行",
		AgentKey: "mock-runner",
	})
	if err != nil {
		t.Fatalf("execute internal query: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", body)
	}
}

func newServerFromFixture(t *testing.T, fixture testFixture) *Server {
	t.Helper()
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
	return server
}

type testFixture struct {
	server          *Server
	cfg             config.Config
	chats           chat.Store
	memories        memory.Store
	registry        catalog.Registry
	runs            contracts.RunManager
	agent           contracts.AgentEngine
	tools           contracts.ToolExecutor
	sandbox         contracts.SandboxClient
	mcp             contracts.McpClient
	hitl            *hitl.Registry
	viewport        contracts.ViewportClient
	catalogReloader contracts.CatalogReloader
}

type testFixtureOptions struct {
	sandbox      contracts.SandboxClient
	configure    func(*config.Config)
	setupRuntime func(root string, cfg *config.Config)
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
	return newTestFixtureWithModelHandlerAndOptions(t, modelHandler, testFixtureOptions{})
}

func newTestFixtureWithModelHandlerAndOptions(t *testing.T, modelHandler http.HandlerFunc, options testFixtureOptions) testFixture {
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
		"  tools:",
		"    - _datetime_",
		"    - _ask_user_question_",
		"    - _ask_user_approval_",
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
		"budget:",
		"  tool:",
		"    timeoutMs: 210000",
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
	if options.configure != nil {
		options.configure(&cfg)
	}
	if options.setupRuntime != nil {
		options.setupRuntime(root, &cfg)
	}

	chats, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(cfg.Paths.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	sandboxClient := options.sandbox
	if sandboxClient == nil {
		sandboxClient = contracts.NewNoopSandboxClient()
	}
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, memories)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	mcp := contracts.NewNoopMcpClient()
	frontendRegistry := frontendtools.NewDefaultRegistry()
	toolExecutor := tools.NewToolRouter(backendTools, mcp, nil, llm.NewFrontendSubmitCoordinator(frontendRegistry), contracts.NewNoopActionInvoker())
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	var hitlRegistry *hitl.Registry
	if cfg.BashHITL.Enabled {
		hitlRegistry, err = hitl.NewRegistry(filepath.Join(cfg.Paths.RegistriesDir, "bash-hitl"))
		if err != nil {
			t.Fatalf("new hitl registry: %v", err)
		}
	}
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, nil, hitlRegistry)

	runs := runctl.NewInMemoryRunManager()
	sandbox := sandboxClient
	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, frontendRegistry, sandbox, hitlRegistry)
	viewport := contracts.NewNoopViewportClient()
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
		HITL:            hitlRegistry,
		FrontendTools:   frontendRegistry,
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
		hitl:            hitlRegistry,
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

func mustEncryptProviderAPIKeyForServerTest(t *testing.T, envPart string, plaintext string) string {
	t.Helper()

	const providerAPIKeyCodePart = "zenmind-provider"

	sum := sha256.Sum256([]byte(providerAPIKeyCodePart + ":" + envPart))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}

	nonce := []byte("0123456789ab")
	data := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(append([]byte{}, nonce...), data...)
	return "AES(" + base64.RawURLEncoding.EncodeToString(payload) + ")"
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
