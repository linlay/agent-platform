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
	"agent-platform/internal/chat"
)

func TestHandleCompactWritesCheckpointAndReplaysEvent(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"模型摘要：已读取日志并保留下一步。"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":5}}`,
			`[DONE]`,
		)
	})
	store := fixture.chats.(*chat.FileStore)
	if _, _, err := store.EnsureChat("chat-compact-api", "mock-agent", "", "第一轮"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	seedServerCompactRun(t, store, "chat-compact-api", "run-1", "第一轮", "bash", strings.Repeat("log\n", 1200), now)
	seedServerCompactRun(t, store, "chat-compact-api", "run-2", "第二轮", "", "第二轮结果", now+1)
	seedServerCompactRun(t, store, "chat-compact-api", "run-3", "第三轮", "", "第三轮结果", now+2)

	body := bytes.NewBufferString(`{"requestId":"req_compact","chatId":"chat-compact-api"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/compact", body)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.Accepted || resp.Data.SummarySource != "model" || resp.Data.ToolDigestCount != 1 {
		t.Fatalf("unexpected compact response %#v", resp.Data)
	}
	if resp.Data.CompactionUsage["totalTokens"] != float64(10) {
		t.Fatalf("expected compaction usage, got %#v", resp.Data.CompactionUsage)
	}

	detail, err := store.LoadChat("chat-compact-api")
	if err != nil {
		t.Fatal(err)
	}
	foundCompactEvent := false
	for _, event := range detail.Events {
		if event.Type == "context.compact.complete" {
			foundCompactEvent = true
			break
		}
	}
	if !foundCompactEvent {
		t.Fatalf("expected replayed compact event, got %#v", detail.Events)
	}
	raw, err := store.LoadRawMessages("chat-compact-api", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || !strings.Contains(strings.TrimSpace(raw[0]["content"].(string)), "模型摘要") {
		t.Fatalf("expected projected compact summary first, got %#v", raw)
	}
}

func TestHandleCompactPostEstimateUsesSummaryProjection(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"短摘要：已经完成长文生成，后续可继续改写。"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":20,"total_tokens":920}}`,
			`[DONE]`,
		)
	})
	store := fixture.chats.(*chat.FileStore)
	if _, _, err := store.EnsureChat("chat-compact-estimate", "mock-agent", "", "第一轮"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	seedServerCompactRun(t, store, "chat-compact-estimate", "run-1", "写一篇长文", "", strings.Repeat("很长的正文内容。", 1200), now)
	seedServerCompactRun(t, store, "chat-compact-estimate", "run-2", "继续", "", "第二轮结果", now+1)
	seedServerCompactRun(t, store, "chat-compact-estimate", "run-3", "收尾", "", "第三轮结果", now+2)

	body := bytes.NewBufferString(`{"requestId":"req_compact_estimate","chatId":"chat-compact-estimate"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/compact", body)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.PreCompactTokens <= 0 || resp.Data.PostCompactTokens <= 0 {
		t.Fatalf("expected compact estimates, got %#v", resp.Data)
	}
	if resp.Data.PostCompactTokens >= resp.Data.PreCompactTokens {
		t.Fatalf("expected post compact estimate to shrink after summary projection, got pre=%d post=%d", resp.Data.PreCompactTokens, resp.Data.PostCompactTokens)
	}
	if resp.Data.CompressionRatio <= 0 || resp.Data.CompressionRatio >= 1 {
		t.Fatalf("expected compression ratio below 1, got %#v", resp.Data)
	}
}

func TestHandleCompactPostEstimateIncludesStaticPromptOverhead(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"短摘要。"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`,
			`[DONE]`,
		)
	})
	store := fixture.chats.(*chat.FileStore)
	if _, _, err := store.EnsureChat("chat-compact-overhead", "mock-agent", "", "第一轮"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	seedServerCompactRun(t, store, "chat-compact-overhead", "run-1", "第一轮", "", "第一轮结果", now)
	seedServerCompactRun(t, store, "chat-compact-overhead", "run-2", "第二轮", "", "第二轮结果", now+1)
	seedServerCompactRun(t, store, "chat-compact-overhead", "run-3", "第三轮", "", "第三轮结果", now+2)

	body := bytes.NewBufferString(`{"requestId":"req_compact_overhead","chatId":"chat-compact-overhead"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/compact", body)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.PostCompactTokens < 500 {
		t.Fatalf("expected post compact estimate to include static system/tool prompt overhead, got %#v", resp.Data)
	}
}

func seedServerCompactRun(t *testing.T, store *chat.FileStore, chatID string, runID string, userText string, toolName string, result string, ts int64) {
	t.Helper()
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: ts,
		Query:     map[string]any{"role": "user", "message": userText},
		Type:      "query",
	}); err != nil {
		t.Fatal(err)
	}
	messages := []chat.StoredMessage{{
		Role:    "assistant",
		Content: []chat.ContentPart{{Type: "text", Text: result}},
	}}
	if toolName != "" {
		messages = []chat.StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []chat.StoredToolCall{{
					ID:   "tool-" + runID,
					Type: "function",
					Function: chat.StoredFunction{
						Name:      toolName,
						Arguments: `{"command":"cat logs"}`,
					},
				}},
			},
			{
				Role:       "tool",
				Name:       toolName,
				ToolCallID: "tool-" + runID,
				Content:    []chat.ContentPart{{Type: "text", Text: result}},
			},
		}
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: ts,
		Type:      "react",
		Messages:  messages,
	}); err != nil {
		t.Fatal(err)
	}
}
