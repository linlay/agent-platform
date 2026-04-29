package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/memory"
)

func TestHandleMemoryScopesReturnsEditableScopes(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_user_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  100,
		UpdatedAt:  200,
	})
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_team_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  110,
		UpdatedAt:  210,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/scopes?agentKey=mock-runner&userKey=alice", nil)
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

func TestHandleMemoryContextPreviewReturnsInjectedMemory(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	if _, _, err := fixture.chats.EnsureChat("chat-preview", "mock-runner", "team-1", "memory preview"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_agent_release",
		AgentKey:   "mock-runner",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeAgent,
		ScopeKey:   "agent:mock-runner",
		Title:      "Desktop builtin release",
		Summary:    "desktop builtin 发布流程是先 make release-program，再同步 desktop assets。",
		SourceType: "tool-write",
		Category:   memory.CategoryWorkflow,
		Importance: 9,
		Confidence: 0.95,
		Status:     memory.StatusActive,
		CreatedAt:  100,
		UpdatedAt:  200,
	})
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_chat_release",
		AgentKey:   "mock-runner",
		Kind:       memory.KindObservation,
		ScopeType:  memory.ScopeChat,
		ScopeKey:   "chat:chat-preview",
		ChatID:     "chat-preview",
		Title:      "desktop builtin 发布排查",
		Summary:    "desktop builtin 发布流程需要确认 VERSION 和 dist/release 输出。",
		SourceType: "learn",
		Category:   memory.CategoryWorkflow,
		Importance: 8,
		Confidence: 0.75,
		Status:     memory.StatusOpen,
		CreatedAt:  110,
		UpdatedAt:  210,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/memory/context/preview", bytes.NewBufferString(`{"chatId":"chat-preview","message":"desktop builtin 发布流程"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryContextPreviewResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Data.Enabled || resp.Data.AgentKey != "mock-runner" || resp.Data.ChatID != "chat-preview" {
		t.Fatalf("unexpected preview envelope: %#v", resp.Data)
	}
	if !strings.Contains(resp.Data.Prompts.Stable, "make release-program") {
		t.Fatalf("expected stable prompt to include release memory, got %q", resp.Data.Prompts.Stable)
	}
	if len(resp.Data.Layers) == 0 || resp.Data.Summary.StableCount == 0 {
		t.Fatalf("expected preview layers and stable summary, got %#v", resp.Data)
	}
	if resp.Data.Layers[0].Items[0].ID == "" || resp.Data.Layers[0].Items[0].Importance == 0 {
		t.Fatalf("expected memory item details, got %#v", resp.Data.Layers[0].Items)
	}
}

func TestHandleMemoryScopeReturnsMarkdownAndRecords(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_user_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  100,
		UpdatedAt:  200,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/scope?agentKey=mock-runner&scopeType=user&scopeKey=user:alice", nil)
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

	reqBody := `{"agentKey":"mock-runner","scopeType":"user","markdown":"# USER\n\n- [new] 偏好中文输出\n  importance: 99\n  content: xxx"}`
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
		AgentKey:   "mock-runner",
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
		CreatedAt:  100,
		UpdatedAt:  200,
	})

	reqBody := `{
	  "agentKey":"mock-runner",
	  "scopeType":"user",
	  "scopeKey":"user:alice",
	  "mode":"markdown",
	  "markdown":"# USER\n\n- [mem_user_1] 偏好中文输出\n  category: general\n  importance: 9\n  confidence: 0.95\n  tags: preference\n  content: 偏好中文输出，术语保持准确。\n\n- [new] 默认先给结论再解释\n  category: response_style\n  importance: 7\n  confidence: 0.9\n  tags: style\n  content: 回答时先给结论，再展开解释。\n",
	  "archiveMissing":true
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/memory/scope", bytes.NewBufferString(reqBody))
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
	record, err := fixture.memories.ReadDetail("mock-runner", "mem_user_1")
	if err != nil {
		t.Fatalf("read updated detail: %v", err)
	}
	if record == nil || record.Importance != 9 {
		t.Fatalf("unexpected updated record: %#v", record)
	}
	results, err := memory.ListConsoleRecords(fixture.memories, memory.RecordFilter{AgentKey: "mock-runner", ScopeType: memory.ScopeUser, Limit: 20})
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

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_fact_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  100,
		UpdatedAt:  200,
	})
	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_obs_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  110,
		UpdatedAt:  210,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/records?agentKey=mock-runner&kind=observation", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryRecords(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryRecordsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Count != 1 || len(resp.Data.Results) != 1 || resp.Data.Results[0].ID != "mem_obs_1" {
		t.Fatalf("unexpected records response: %#v", resp.Data)
	}
}

func TestHandleMemoryRecordReturnsRawFields(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	server := fixture.server

	writeTestMemory(t, fixture.memories, api.StoredMemoryResponse{
		ID:         "mem_obs_1",
		AgentKey:   "mock-runner",
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
		CreatedAt:  110,
		UpdatedAt:  210,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/memory/record?agentKey=mock-runner&id=mem_obs_1", nil)
	rec := httptest.NewRecorder()
	server.handleMemoryRecord(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.MemoryRecordDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.SourceTable != "MEMORY_OBSERVATIONS" {
		t.Fatalf("unexpected source table: %#v", resp.Data)
	}
	if _, ok := resp.Data.RawFields["runId"]; !ok {
		t.Fatalf("expected runId in rawFields, got %#v", resp.Data.RawFields)
	}
}

func writeTestMemory(t *testing.T, store memory.Store, item api.StoredMemoryResponse) {
	t.Helper()
	if err := store.Write(item); err != nil {
		t.Fatalf("write memory %s: %v", item.ID, err)
	}
}
