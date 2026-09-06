package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestHandleMemoryScopesReturnsEditableScopes(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, server.deps.Memory, api.StoredMemoryResponse{
		ID:         "mem_user_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeUser,
		ScopeKey:   "user:alice",
		Title:      "偏好中文输出",
		Summary:    "偏好中文输出，术语保持准确。",
		SourceType: "tool-write",
		Category:   "general",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  testEpochMillis + 100,
		UpdatedAt:  testEpochMillis + 200,
	})
	writeTestMemory(t, server.deps.Memory, api.StoredMemoryResponse{
		ID:         "mem_team_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeTeam,
		ScopeKey:   "team:platform",
		Title:      "周会固定周三",
		Summary:    "团队周会固定在周三上午。",
		SourceType: "tool-write",
		Category:   "workflow",
		Importance: 7,
		Confidence: 0.9,
		Status:     memory.StatusActive,
		CreatedAt:  testEpochMillis + 110,
		UpdatedAt:  testEpochMillis + 210,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/scope/list?agentKey=mock-agent&userKey=alice", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryScopes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryScopesResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data.Scopes) != 4 {
		t.Fatalf("expected 4 scopes, got %#v", resp.Data.Scopes)
	}
	if resp.Data.Scopes[0].ScopeType != memory.ScopeUser || resp.Data.Scopes[0].RecordCount != 1 {
		t.Fatalf("unexpected user scope: %#v", resp.Data.Scopes[0])
	}
}

func TestHandleMemoryMetaReturnsFrontendEnums(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/memory/meta", nil)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryMetaResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !containsString(resp.Data.Categories, memory.CategoryPreference) || !containsString(resp.Data.Categories, memory.CategoryUnresolvedIssue) {
		t.Fatalf("expected standard categories, got %#v", resp.Data.Categories)
	}
	if !containsString(resp.Data.Types, memory.KindFact) || !containsString(resp.Data.Types, memory.KindObservation) {
		t.Fatalf("expected memory types, got %#v", resp.Data.Types)
	}
	if !containsString(resp.Data.ScopeTypes, memory.ScopeUser) || !containsString(resp.Data.ScopeTypes, memory.ScopeChat) {
		t.Fatalf("expected scope types, got %#v", resp.Data.ScopeTypes)
	}
	if !containsString(resp.Data.Statuses, memory.StatusActive) || !containsString(resp.Data.Statuses, memory.StatusArchived) {
		t.Fatalf("expected statuses, got %#v", resp.Data.Statuses)
	}
	if !containsString(resp.Data.SourceTypes, "tool-write") || !containsString(resp.Data.SourceTypes, "console-edit") {
		t.Fatalf("expected source types, got %#v", resp.Data.SourceTypes)
	}
}

func TestHandleMemoryScopeReturnsMarkdownAndRecords(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_user_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeUser,
		ScopeKey:   "user:alice",
		Title:      "偏好中文输出",
		Summary:    "偏好中文输出，术语保持准确。",
		SourceType: "tool-write",
		Category:   "general",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  testEpochMillis + 100,
		UpdatedAt:  testEpochMillis + 200,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/scope/detail?agentKey=mock-agent&scopeType=user&scopeKey=user:alice", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryScope(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryScopeDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Data.Markdown, "[mem_user_1] 偏好中文输出") {
		t.Fatalf("unexpected markdown: %q", resp.Data.Markdown)
	}
	if len(resp.Data.Records) != 1 || resp.Data.Records[0].ID != "mem_user_1" {
		t.Fatalf("unexpected records: %#v", resp.Data.Records)
	}
}

func TestHandleMemoryScopeValidateRejectsBadImportance(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	reqBody := `{"agentKey":"mock-agent","scopeType":"user","markdown":"# USER\n\n- [new] 偏好中文输出\n  importance: 99\n  content: xxx"}`
	req := httptest.NewRequest(http.MethodPost, "/api/memory/scope/validate", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleMemoryScopeValidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryScopeValidateResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Valid || len(resp.Data.Errors) == 0 {
		t.Fatalf("expected validation errors, got %#v", resp.Data)
	}
}

func TestHandleMemoryScopeSaveUpdatesAndCreatesFacts(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_user_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeUser,
		ScopeKey:   "user:alice",
		Title:      "偏好中文输出",
		Summary:    "偏好中文输出。",
		SourceType: "tool-write",
		Category:   "general",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  testEpochMillis + 100,
		UpdatedAt:  testEpochMillis + 200,
	})

	reqBody := `{
	  "agentKey":"mock-agent",
	  "scopeType":"user",
	  "scopeKey":"user:alice",
	  "mode":"markdown",
	  "markdown":"# USER\n\n- [mem_user_1] 偏好中文输出\n  category: general\n  importance: 9\n  confidence: 0.95\n  tags: preference\n  content: 偏好中文输出，术语保持准确。\n\n- [new] 默认先给结论再解释\n  category: response_style\n  importance: 7\n  confidence: 0.9\n  tags: style\n  content: 回答时先给结论，再展开解释。\n",
	  "archiveMissing":true
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/memory/scope/save", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleMemoryScopeSave(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryScopeSaveResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Summary.Created != 1 || resp.Data.Summary.Updated != 1 {
		t.Fatalf("unexpected save summary: %#v", resp.Data.Summary)
	}
	record, err := fixture.memories.ReadDetail("mock-agent", "mem_user_1")
	if err != nil {
		t.Fatalf("read updated detail: %v", err)
	}
	if record == nil || record.Importance != 9 {
		t.Fatalf("unexpected updated record: %#v", record)
	}
	results, err := memory.ListConsoleRecords(fixture.memories, memory.RecordFilter{AgentKey: "mock-agent", ScopeType: memory.ScopeUser, Limit: 20})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if results.Count != 2 {
		t.Fatalf("expected two user records after save, got %#v", results)
	}
}

func TestHandleMemoryRecordsFiltersResults(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server
	now := time.Now().UnixMilli()

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_fact_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeUser,
		ScopeKey:   "user:alice",
		Title:      "偏好中文输出",
		Summary:    "偏好中文输出。",
		SourceType: "tool-write",
		Category:   "general",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  now - 100,
		UpdatedAt:  now - 100,
	})
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_obs_1",
		AgentKey:   "mock-agent",
		ChatID:     "chat-1",
		Kind:       memory.KindObservation,
		ScopeType:  memory.ScopeChat,
		ScopeKey:   "chat:chat-1",
		Title:      "修复权限问题",
		Summary:    "修复了权限问题。",
		SourceType: "learn",
		Category:   "bugfix",
		Importance: 8,
		Confidence: 0.75,
		Status:     memory.StatusOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/record/list?agentKey=mock-agent&kind=fact", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryRecords(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryRecordsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Count != 0 || len(resp.Data.Results) != 0 {
		t.Fatalf("unexpected records response: %#v", resp.Data)
	}
}

func TestMemoryWSRecordsMirrorsHTTP(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	enableMemoryFixtureWebSocket(t, fixture.server)

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_fact_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeAgent,
		ScopeKey:   "agent:mock-agent",
		Title:      "偏好中文输出",
		Summary:    "用户偏好中文输出。",
		SourceType: "manual",
		Category:   "preference",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  testEpochMillis + 100,
		UpdatedAt:  testEpochMillis + 200,
	})
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_obs_1",
		AgentKey:   "mock-agent",
		ChatID:     "chat-1",
		Kind:       memory.KindObservation,
		ScopeType:  memory.ScopeChat,
		ScopeKey:   "chat:chat-1",
		Title:      "修复权限问题",
		Summary:    "修复了权限问题。",
		SourceType: "learn",
		Category:   "bugfix",
		Importance: 8,
		Confidence: 0.75,
		Status:     memory.StatusOpen,
		CreatedAt:  testEpochMillis + 110,
		UpdatedAt:  testEpochMillis + 210,
	})

	conn := dialMemoryWebSocket(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/memory/record/list",
		ID:    "records",
		Payload: marshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"kind":     "fact",
		}),
	}); err != nil {
		t.Fatalf("write records request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read records response: %v", err)
	}
	records, err := marshalAutomationResponseData[api.MemoryRecordsResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode records data: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.ID != "records" || records.Count != 1 || len(records.Results) != 1 || records.Results[0].ID != "mem_fact_1" {
		t.Fatalf("unexpected records frame %#v data=%#v", frame, records)
	}
}

func TestMemoryWSRecordAndMeta(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	enableMemoryFixtureWebSocket(t, fixture.server)

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_obs_1",
		AgentKey:   "mock-agent",
		ChatID:     "chat-1",
		Kind:       memory.KindObservation,
		ScopeType:  memory.ScopeChat,
		ScopeKey:   "chat:chat-1",
		Title:      "修复权限问题",
		Summary:    "修复了权限问题。",
		SourceType: "learn",
		Category:   "bugfix",
		Importance: 8,
		Confidence: 0.75,
		Status:     memory.StatusOpen,
		CreatedAt:  testEpochMillis + 110,
		UpdatedAt:  testEpochMillis + 210,
	})

	conn := dialMemoryWebSocket(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/memory/record/detail",
		ID:    "record",
		Payload: marshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"id":       "mem_obs_1",
		}),
	}); err != nil {
		t.Fatalf("write record request: %v", err)
	}
	var recordFrame ws.ResponseFrame
	if err := conn.ReadJSON(&recordFrame); err != nil {
		t.Fatalf("read record response: %v", err)
	}
	record, err := marshalAutomationResponseData[api.MemoryRecordDetailResponse](recordFrame.Data)
	if err != nil {
		t.Fatalf("decode record data: %v", err)
	}
	if recordFrame.Frame != ws.FrameResponse || recordFrame.ID != "record" || record.ID != "mem_obs_1" || record.SourceTable != "MEMORY_OBSERVATIONS" {
		t.Fatalf("unexpected record frame %#v data=%#v", recordFrame, record)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/memory/meta",
		ID:      "meta",
		Payload: marshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write meta request: %v", err)
	}
	var metaFrame ws.ResponseFrame
	if err := conn.ReadJSON(&metaFrame); err != nil {
		t.Fatalf("read meta response: %v", err)
	}
	meta, err := marshalAutomationResponseData[api.MemoryMetaResponse](metaFrame.Data)
	if err != nil {
		t.Fatalf("decode meta data: %v", err)
	}
	if metaFrame.Frame != ws.FrameResponse || metaFrame.ID != "meta" || len(meta.Types) == 0 || len(meta.ScopeTypes) == 0 {
		t.Fatalf("unexpected meta frame %#v data=%#v", metaFrame, meta)
	}
}

func TestHandleMemoryRecordReturnsRawFields(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server
	now := time.Now().UnixMilli()

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_fact_1",
		AgentKey:   "mock-agent",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeAgent,
		ScopeKey:   "agent:mock-agent",
		Title:      "修复权限问题",
		Summary:    "修复了权限问题。",
		SourceType: "tool-write",
		Category:   "bugfix",
		Importance: 8,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/record/detail?agentKey=mock-agent&id=mem_fact_1", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryRecord(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryRecordDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.SourceTable != "MEMORY_FACTS" {
		t.Fatalf("unexpected source table: %#v", resp.Data)
	}
	if resp.Data.RawFields["sourceKind"] != "tool-write" || resp.Data.RawFields["sourceRef"] != "mem_fact_1" {
		t.Fatalf("expected SQLite fact rawFields, got %#v", resp.Data.RawFields)
	}
}

func writeTestMemory(t *testing.T, store memory.Store, item api.StoredMemoryResponse) {
	t.Helper()
	if err := store.Write(item); err != nil {
		t.Fatalf("write memory %s: %v", item.ID, err)
	}
}

func enableMemoryFixtureWebSocket(t *testing.T, server *Server) {
	t.Helper()
	hub := ws.NewHub()
	t.Cleanup(func() { hub.CloseAll(gws.CloseNormalClosure, "test done") })
	server.deps.Config.WebSocket.WriteQueueSize = 4
	server.deps.Config.WebSocket.PingInterval = 30000
	server.wsHandler = server.newWSHandler(hub)
	server.router.Handle("/ws", server.wsHandler)
}

func dialMemoryWebSocket(t *testing.T, handler http.Handler) *gws.Conn {
	t.Helper()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	readAutomationConnectedPush(t, conn)
	return conn
}

func TestRetiredMemoryRoutesAreUnavailable(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	enableMemoryFixtureWebSocket(t, fixture.server)
	conn := dialMemoryWebSocket(t, fixture.server)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/learn", "/api/memory/context-preview"} {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{"chatId":"unused","message":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("retired HTTP route returned %d: %s", rec.Code, rec.Body.String())
			}
			if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: route, ID: route, Payload: marshalPayload(map[string]any{"chatId": "unused", "message": "hello"})}); err != nil {
				t.Fatal(err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if frame.Frame != ws.FrameError || frame.ID != route || frame.Code != http.StatusBadRequest {
				t.Fatalf("retired WS route returned %#v", frame)
			}
		})
	}
	items, err := fixture.memories.List("", "", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("retired routes wrote memory: %#v", items)
	}
}
