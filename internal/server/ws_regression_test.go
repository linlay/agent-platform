package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/stream"
)

func wsRegressionEpochMillis(value int64) *int64 {
	return &value
}

func TestServerSharedHelpersUseCommonChatAndMemoryStores(t *testing.T) {
	server, chats, memories := newServerForHelperTests(t)

	if _, _, err := chats.EnsureChat("chat-1", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 1_001
	startServerFixtureRun(t, chats, "chat-1", "run-1", startedAt)
	if err := chats.AppendQueryLine("chat-1", chat.QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: startedAt,
		Query: map[string]any{
			"chatId":  "chat-1",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-1", chat.StepLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: startedAt + 1,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "user", Content: []chat.ContentPart{{Type: "text", Text: "hello"}}, Ts: wsRegressionEpochMillis(startedAt + 1)},
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "answer"}}, Ts: wsRegressionEpochMillis(startedAt + 2)},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-1",
		RunID:           "run-1",
		AssistantText:   "answer",
		InitialMessage:  "hello",
		StartedAtMillis: startedAt,
		UpdatedAtMillis: startedAt + 3,
		Usage: chat.UsageData{
			PromptTokens:           3,
			CompletionTokens:       5,
			TotalTokens:            8,
			CachedTokens:           2,
			ReasoningTokens:        4,
			PromptCacheHitTokens:   2,
			PromptCacheMissTokens:  1,
			LlmChatCompletionCount: 1,
		},
	}); err != nil {
		t.Fatalf("persist run completion: %v", err)
	}

	summaries, err := server.listChatSummaries("", "")
	if err != nil {
		t.Fatalf("list chat summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary, got %#v", summaries)
	}
	if summaries[0].LastRunID != "run-1" || summaries[0].Usage == nil || summaries[0].Usage.TotalTokens != 8 {
		t.Fatalf("unexpected chat summary %#v", summaries[0])
	}
	if summaries[0].Usage.PromptTokensDetails == nil || summaries[0].Usage.PromptTokensDetails.CacheHitTokens != 2 ||
		summaries[0].Usage.PromptTokensDetails.CacheMissTokens != 1 ||
		summaries[0].Usage.CompletionTokensDetails == nil || summaries[0].Usage.CompletionTokensDetails.ReasoningTokens != 4 ||
		summaries[0].Usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat summary usage, got %#v", summaries[0].Usage)
	}
	if summaries[0].Read.IsRead {
		t.Fatalf("expected completed chat to be unread, got %#v", summaries[0].Read)
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-1", true)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ChatID != "chat-1" || len(detail.Events) == 0 || len(detail.RawMessages) < 2 {
		t.Fatalf("unexpected chat detail %#v", detail)
	}
	if detail.Usage == nil || detail.Usage.LastRun == nil || detail.Usage.Chat == nil {
		t.Fatalf("expected detailed chat detail usage breakdown, got %#v", detail.Usage)
	}
	if detail.Usage.LastRun.PromptTokensDetails == nil || detail.Usage.LastRun.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Usage.LastRun.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Usage.LastRun.CompletionTokensDetails == nil || detail.Usage.LastRun.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Usage.LastRun.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat detail usage, got %#v", detail.Usage)
	}
	if detail.Usage.Chat.PromptTokensDetails == nil || detail.Usage.Chat.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Usage.Chat.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Usage.Chat.CompletionTokensDetails == nil || detail.Usage.Chat.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Usage.Chat.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat cumulative usage, got %#v", detail.Usage)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Usage.PromptTokensDetails == nil || detail.Runs[0].Usage.PromptTokensDetails.CacheHitTokens != 2 ||
		detail.Runs[0].Usage.PromptTokensDetails.CacheMissTokens != 1 ||
		detail.Runs[0].Usage.CompletionTokensDetails == nil || detail.Runs[0].Usage.CompletionTokensDetails.ReasoningTokens != 4 ||
		detail.Runs[0].Usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed run summary usage, got %#v", detail.Runs)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/learn", strings.NewReader(`{"requestId":"req-learn","chatId":"chat-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleLearn(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("learn expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var learnResp api.ApiResponse[api.LearnResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &learnResp); err != nil {
		t.Fatalf("decode learn response: %v", err)
	}
	if !learnResp.Data.Accepted || learnResp.Data.ObservationCount == 0 {
		t.Fatalf("unexpected learn response %#v", learnResp.Data)
	}
	matches, err := memories.Search("answer", 10)
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected stored memory, got %#v", matches)
	}
}

func TestLoadChatDetailUsageBreakdownSeparatesLastRunFromChatTotal(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)

	if _, _, err := chats.EnsureChat("chat-usage-breakdown", "agent-1", "", "first"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := completeServerFixtureRun(t, chats, chat.RunCompletion{
		ChatID:          "chat-usage-breakdown",
		RunID:           "run-usage-1",
		InitialMessage:  "first",
		AssistantText:   "first answer",
		UpdatedAtMillis: testEpochMillis + 2_001,
		Usage: chat.UsageData{
			PromptTokens:           10,
			CompletionTokens:       5,
			TotalTokens:            15,
			LlmChatCompletionCount: 1,
		},
	}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	if err := completeServerFixtureRun(t, chats, chat.RunCompletion{
		ChatID:          "chat-usage-breakdown",
		RunID:           "run-usage-2",
		InitialMessage:  "second",
		AssistantText:   "second answer",
		UpdatedAtMillis: testEpochMillis + 2_003,
		Usage: chat.UsageData{
			PromptTokens:           7,
			CompletionTokens:       3,
			TotalTokens:            10,
			ReasoningTokens:        2,
			LlmChatCompletionCount: 1,
		},
	}); err != nil {
		t.Fatalf("complete second run: %v", err)
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-usage-breakdown", false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.Usage == nil || detail.Usage.LastRun == nil || detail.Usage.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", detail.Usage)
	}
	if detail.Usage.LastRun.PromptTokens != 7 || detail.Usage.LastRun.CompletionTokens != 3 ||
		detail.Usage.LastRun.TotalTokens != 10 || detail.Usage.LastRun.LlmChatCompletionCount != 1 {
		t.Fatalf("expected last run usage, got %#v", detail.Usage.LastRun)
	}
	if detail.Usage.LastRun.CompletionTokensDetails == nil || detail.Usage.LastRun.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("expected last run detail usage, got %#v", detail.Usage.LastRun)
	}
	if detail.Usage.Chat.PromptTokens != 17 || detail.Usage.Chat.CompletionTokens != 8 ||
		detail.Usage.Chat.TotalTokens != 25 || detail.Usage.Chat.LlmChatCompletionCount != 2 {
		t.Fatalf("expected chat cumulative usage, got %#v", detail.Usage.Chat)
	}
	if len(detail.Runs) != 2 || detail.Runs[0].RunID != "run-usage-2" || detail.Runs[0].Usage.TotalTokens != 10 {
		t.Fatalf("expected latest run first, got %#v", detail.Runs)
	}
}

func TestLoadChatDetailReturnsNotFoundAcrossHTTP(t *testing.T) {
	server, _, _ := newServerForHelperTests(t)

	if _, err := server.loadChatDetail(context.Background(), "missing-chat", false); err == nil {
		t.Fatalf("expected loadChatDetail to return not found")
	}

	chatRec := httptest.NewRecorder()
	server.handleChat(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=missing-chat", nil))
	if chatRec.Code != http.StatusNotFound {
		t.Fatalf("expected HTTP chat 404, got %d: %s", chatRec.Code, chatRec.Body.String())
	}
}

func TestLoadChatDetailIncludesActiveRunAndConflictReturnsHTTP409(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	runs := contracts.NewInMemoryRunManager()
	server.deps.Runs = runs

	if _, _, err := chats.EnsureChat("chat-live", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	doneStartedAt := testEpochMillis + 10_001
	startServerFixtureRun(t, chats, "chat-live", "run-done", doneStartedAt)
	if err := chats.AppendQueryLine("chat-live", chat.QueryLine{
		ChatID:    "chat-live",
		RunID:     "run-done",
		UpdatedAt: doneStartedAt,
		Query: map[string]any{
			"chatId":  "chat-live",
			"message": "completed",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append completed query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-live", chat.StepLine{
		ChatID:    "chat-live",
		RunID:     "run-done",
		UpdatedAt: doneStartedAt + 1,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "done"}}, Ts: wsRegressionEpochMillis(doneStartedAt + 1)},
		},
	}); err != nil {
		t.Fatalf("append completed step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-live",
		RunID:           "run-done",
		AssistantText:   "done",
		InitialMessage:  "completed",
		StartedAtMillis: doneStartedAt,
		UpdatedAtMillis: doneStartedAt + 2,
	}); err != nil {
		t.Fatalf("complete run-done: %v", err)
	}
	liveStartedAt := testEpochMillis + 10_003
	startServerFixtureRun(t, chats, "chat-live", "run-live", liveStartedAt)
	if err := chats.AppendQueryLine("chat-live", chat.QueryLine{
		ChatID:    "chat-live",
		RunID:     "run-live",
		UpdatedAt: liveStartedAt,
		Query: map[string]any{
			"chatId":       "chat-live",
			"message":      "still running",
			"planningMode": true,
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append live query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-live", chat.StepLine{
		ChatID:    "chat-live",
		RunID:     "run-live",
		UpdatedAt: liveStartedAt + 1,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "partial"}}, Ts: wsRegressionEpochMillis(liveStartedAt + 1)},
		},
	}); err != nil {
		t.Fatalf("append live step line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live",
		ChatID:   "chat-live",
		AgentKey: "agent-1",
	})

	detail, err := server.loadChatDetail(context.Background(), "chat-live", false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.RunID != "run-live" {
		t.Fatalf("expected active run in chat detail, got %#v", detail.ActiveRun)
	}
	if !detail.ActiveRun.PlanningMode {
		t.Fatalf("expected active run planningMode=true, got %#v", detail.ActiveRun)
	}
	runCompleteCounts := map[string]int{}
	for _, event := range detail.Events {
		if event.Type != "run.complete" {
			continue
		}
		runCompleteCounts[event.String("runId")]++
	}
	if runCompleteCounts["run-live"] != 0 {
		t.Fatalf("expected active run.complete to be removed, got %#v", detail.Events)
	}
	if runCompleteCounts["run-done"] != 1 {
		t.Fatalf("expected completed run.complete to remain, got %#v", detail.Events)
	}

	if _, _, err := chats.EnsureChat("chat-live-plain", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure plain chat: %v", err)
	}
	plainStartedAt := testEpochMillis + 10_005
	startServerFixtureRun(t, chats, "chat-live-plain", "run-live-plain", plainStartedAt)
	if err := chats.AppendQueryLine("chat-live-plain", chat.QueryLine{
		ChatID:    "chat-live-plain",
		RunID:     "run-live-plain",
		UpdatedAt: plainStartedAt,
		Query: map[string]any{
			"chatId":  "chat-live-plain",
			"message": "plain running",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append plain live query line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live-plain",
		ChatID:   "chat-live-plain",
		AgentKey: "agent-1",
	})
	plainDetail, err := server.loadChatDetail(context.Background(), "chat-live-plain", false)
	if err != nil {
		t.Fatalf("load plain chat detail: %v", err)
	}
	if plainDetail.ActiveRun == nil || plainDetail.ActiveRun.PlanningMode {
		t.Fatalf("expected plain active run without planningMode, got %#v", plainDetail.ActiveRun)
	}
	activeRunJSON, err := json.Marshal(plainDetail.ActiveRun)
	if err != nil {
		t.Fatalf("marshal plain active run: %v", err)
	}
	if strings.Contains(string(activeRunJSON), "planningMode") {
		t.Fatalf("expected planningMode to be omitted when false, got %s", string(activeRunJSON))
	}

	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live-2",
		ChatID:   "chat-live",
		AgentKey: "agent-1",
	})

	rec := httptest.NewRecorder()
	server.handleChat(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=chat-live", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Msg != "active_run_conflict" {
		t.Fatalf("expected active_run_conflict, got %#v", resp)
	}
}

func TestActiveRunInPlanningStage(t *testing.T) {
	planningQuery := &chat.QueryLine{Query: map[string]any{"planningMode": true}}
	plainQuery := &chat.QueryLine{Query: map[string]any{"message": "plain"}}
	planningAnswer := func(runID string, decision string) stream.EventData {
		return stream.EventData{
			Type:      "awaiting.answer",
			Timestamp: testEpochMillis,
			Payload: map[string]any{
				"runId": runID,
				"mode":  "planning",
				"planning": map[string]any{
					"decision": decision,
				},
			},
		}
	}

	cases := []struct {
		name    string
		runID   string
		query   *chat.QueryLine
		events  []stream.EventData
		summary *chat.Summary
		want    bool
	}{
		{
			name:  "plain query is never in planning stage",
			runID: "run-plain",
			query: plainQuery,
			events: []stream.EventData{
				planningAnswer("run-plain", "reject"),
			},
			summary: &chat.Summary{PendingAwaiting: &chat.PendingAwaiting{
				RunID: "run-plain",
				Mode:  "planning",
			}},
			want: false,
		},
		{
			name:  "planning query without planning answer remains planning",
			runID: "run-planning",
			query: planningQuery,
			want:  true,
		},
		{
			name:  "latest reject remains planning",
			runID: "run-reject",
			query: planningQuery,
			events: []stream.EventData{
				planningAnswer("run-reject", "reject"),
			},
			want: true,
		},
		{
			name:  "latest approve exits planning",
			runID: "run-approve",
			query: planningQuery,
			events: []stream.EventData{
				planningAnswer("run-approve", "reject"),
				planningAnswer("run-approve", "approve"),
			},
			want: false,
		},
		{
			name:  "other run approve does not affect active run",
			runID: "run-current",
			query: planningQuery,
			events: []stream.EventData{
				planningAnswer("run-other", "approve"),
			},
			want: true,
		},
		{
			name:  "pending planning awaiting keeps active run in planning",
			runID: "run-pending",
			query: planningQuery,
			events: []stream.EventData{
				planningAnswer("run-pending", "approve"),
			},
			summary: &chat.Summary{PendingAwaiting: &chat.PendingAwaiting{
				RunID: "run-pending",
				Mode:  "planning",
			}},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeRunInPlanningStage(tc.runID, tc.query, tc.events, tc.summary); got != tc.want {
				t.Fatalf("activeRunInPlanningStage() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadChatDetailActiveRunPlanningModeReflectsPlanningDecision(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	runs := contracts.NewInMemoryRunManager()
	server.deps.Runs = runs

	chatID := "chat-live-plan-approved"
	runID := "run-live-plan-approved"
	if _, _, err := chats.EnsureChat(chatID, "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 20_001
	startServerFixtureRun(t, chats, chatID, runID, startedAt)
	if err := chats.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: startedAt,
		Query: map[string]any{
			"chatId":       chatID,
			"message":      "plan then execute",
			"planningMode": true,
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendSubmitLine(chatID, chat.SubmitLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: startedAt + 1,
		Type:      "submit",
		Submit: map[string]any{
			"type":       "request.submit",
			"timestamp":  startedAt + 1,
			"chatId":     chatID,
			"awaitingId": "await-planning",
			"params": []any{
				map[string]any{"id": "confirm", "decision": "approve"},
			},
		},
		Answer: map[string]any{
			"type":       "awaiting.answer",
			"timestamp":  startedAt + 1,
			"awaitingId": "await-planning",
			"mode":       "planning",
			"planning": map[string]any{
				"id":         "confirm",
				"planningId": runID + "_planning_1",
				"decision":   "approve",
			},
		},
	}); err != nil {
		t.Fatalf("append submit line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    runID,
		ChatID:   chatID,
		AgentKey: "agent-1",
	})

	detail, err := server.loadChatDetail(context.Background(), chatID, false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.RunID != runID {
		t.Fatalf("expected active run, got %#v", detail.ActiveRun)
	}
	if detail.ActiveRun.PlanningMode {
		t.Fatalf("expected approved plan active run to leave planning mode, got %#v", detail.ActiveRun)
	}
	activeRunJSON, err := json.Marshal(detail.ActiveRun)
	if err != nil {
		t.Fatalf("marshal active run: %v", err)
	}
	if strings.Contains(string(activeRunJSON), "planningMode") {
		t.Fatalf("expected planningMode to be omitted after approval, got %s", string(activeRunJSON))
	}
}

func TestLoadChatDetailActiveRunLastSeqUsesPersistedLiveSeqCursor(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	runs := contracts.NewInMemoryRunManager()
	server.deps.Runs = runs

	if _, _, err := chats.EnsureChat("chat-live-cursor", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	cursorStartedAt := testEpochMillis + 30_001
	startServerFixtureRun(t, chats, "chat-live-cursor", "run-live-cursor", cursorStartedAt)
	if err := chats.AppendQueryLine("chat-live-cursor", chat.QueryLine{
		ChatID:    "chat-live-cursor",
		RunID:     "run-live-cursor",
		UpdatedAt: cursorStartedAt,
		LiveSeq:   1,
		Query: map[string]any{
			"chatId":  "chat-live-cursor",
			"message": "still running",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-live-cursor", chat.StepLine{
		ChatID:    "chat-live-cursor",
		RunID:     "run-live-cursor",
		UpdatedAt: cursorStartedAt + 1,
		LiveSeq:   3,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: "partial"}},
				Ts:      wsRegressionEpochMillis(cursorStartedAt + 1),
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live-cursor",
		ChatID:   "chat-live-cursor",
		AgentKey: "agent-1",
	})
	bus, ok := runs.EventBus("run-live-cursor")
	if !ok {
		t.Fatal("expected run event bus")
	}
	for seq := int64(1); seq <= 672; seq++ {
		eventType := "content.delta"
		if seq == 4 {
			eventType = "tool.start"
		}
		bus.Publish(stream.EventData{
			Seq:       seq,
			Type:      eventType,
			Timestamp: testEpochMillis + 32_000 + seq,
			Payload:   map[string]any{"runId": "run-live-cursor"},
		})
	}
	status, ok := runs.RunStatus("run-live-cursor")
	if !ok || status.LastSeq != 672 {
		t.Fatalf("expected in-memory latest seq 672, got %#v ok=%v", status, ok)
	}

	detail, err := server.loadChatDetail(context.Background(), "chat-live-cursor", false)
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	if detail.ActiveRun == nil {
		t.Fatalf("expected active run, got %#v", detail)
	}
	if detail.ActiveRun.LastSeq != 3 {
		t.Fatalf("expected active run lastSeq from persisted liveSeq=3, got %#v", detail.ActiveRun)
	}

	observer, err := runs.AttachObserver("run-live-cursor", detail.ActiveRun.LastSeq)
	if err != nil {
		t.Fatalf("attach observer: %v", err)
	}
	defer runs.DetachObserver("run-live-cursor", observer.ID)
	select {
	case event := <-observer.Events:
		if event.Seq != 4 || event.Type != "tool.start" {
			t.Fatalf("expected attach replay to resume at seq 4 tool.start, got %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replay event")
	}

	if _, _, err := chats.EnsureChat("chat-old-live-cursor", "agent-1", "", "hello"); err != nil {
		t.Fatalf("ensure old chat: %v", err)
	}
	oldCursorStartedAt := testEpochMillis + 33_001
	startServerFixtureRun(t, chats, "chat-old-live-cursor", "run-old-live-cursor", oldCursorStartedAt)
	if err := chats.AppendQueryLine("chat-old-live-cursor", chat.QueryLine{
		ChatID:    "chat-old-live-cursor",
		RunID:     "run-old-live-cursor",
		UpdatedAt: oldCursorStartedAt,
		Query: map[string]any{
			"chatId":  "chat-old-live-cursor",
			"message": "running",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append old query line: %v", err)
	}
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-old-live-cursor",
		ChatID:   "chat-old-live-cursor",
		AgentKey: "agent-1",
	})
	oldBus, ok := runs.EventBus("run-old-live-cursor")
	if !ok {
		t.Fatal("expected old run event bus")
	}
	oldBus.Publish(stream.EventData{Seq: 9, Type: "content.delta", Timestamp: oldCursorStartedAt + 1, Payload: map[string]any{"runId": "run-old-live-cursor"}})
	oldDetail, err := server.loadChatDetail(context.Background(), "chat-old-live-cursor", false)
	if err != nil {
		t.Fatalf("load old chat detail: %v", err)
	}
	if oldDetail.ActiveRun == nil || oldDetail.ActiveRun.LastSeq != 0 {
		t.Fatalf("expected active run lastSeq=0, got %#v", oldDetail.ActiveRun)
	}
}

func TestBroadcastDefinitionsStayAlignedAcrossHTTPAndWS(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	handlerQuery := mustReadFile(t, filepath.Join(root, "handler_query.go"))
	handlerQueryPrepare := mustReadFile(t, filepath.Join(root, "handler_query_prepare.go"))
	handlerChat := mustReadFile(t, filepath.Join(root, "handler_chat.go"))
	wsRoutes := mustReadFile(t, filepath.Join(root, "ws_routes.go"))
	wsQueryRoutes := mustReadFile(t, filepath.Join(root, "ws_query_routes.go"))

	assertContains(t, handlerQuery, `s.broadcast("run.started"`)
	assertContains(t, handlerQuery, `s.broadcast("run.finished"`)
	assertContains(t, handlerQueryPrepare, `s.broadcast("chat.created"`)
	assertContains(t, handlerChat, `s.broadcastChatReadState("chat.read"`)
	assertContains(t, handlerQuery, `s.broadcastChatReadState("chat.unread"`)
	assertContains(t, wsRoutes, `handler.RegisterRoute("/api/agents", s.wsAgents)`)
	assertContains(t, wsRoutes, `handler.RegisterRoute("/api/attach"`)
	assertContains(t, wsRoutes, `handler.RegisterRoute("/api/resource", s.wsResource)`)
	assertContains(t, wsRoutes, `handler.RegisterRoute("/api/upload", s.wsDownload)`)
	assertNotContains(t, wsRoutes, `handler.RegisterRoute("/api/admin/agents"`)
	assertNotContains(t, wsRoutes, `handler.RegisterRoute("/api/skills"`)
	assertNotContains(t, wsRoutes, `handler.RegisterRoute("/api/tools"`)
	assertNotContains(t, wsRoutes, `handler.RegisterRoute("/api/pull", s.wsDownload)`)
	assertNotContains(t, wsRoutes, `handler.RegisterRoute("/api/push"`)
	assertContains(t, wsQueryRoutes, `s.broadcast("run.started"`)
	assertContains(t, wsQueryRoutes, `s.broadcast("run.finished"`)
	assertContains(t, wsRoutes, `s.broadcastChatReadState("chat.read"`)
	assertContains(t, wsQueryRoutes, `s.broadcastChatReadState("chat.unread"`)
}

func TestGatewayPullAndPushURLBuildersUseDirectionalEndpoints(t *testing.T) {
	if config.GatewayDownloadPath != "/api/pull" {
		t.Fatalf("expected GatewayDownloadPath /api/pull, got %q", config.GatewayDownloadPath)
	}
	if config.GatewayUploadPath != "/api/push" {
		t.Fatalf("expected GatewayUploadPath /api/push, got %q", config.GatewayUploadPath)
	}
	server, _, _ := newServerForHelperTests(t)
	if got := server.buildGatewayURL("https://gateway.example", "ticket-1"); got != "https://gateway.example/api/pull/ticket-1" {
		t.Fatalf("unexpected gateway pull url: %q", got)
	}
	if got := server.buildGatewayPushURL("https://gateway.example", "ticket-1"); got != "https://gateway.example/api/push/ticket-1" {
		t.Fatalf("unexpected gateway push url: %q", got)
	}
	if got := server.buildGatewayPushURL("https://gateway.example", "/api/push/ticket-1?x=1"); got != "https://gateway.example/api/push/ticket-1?x=1" {
		t.Fatalf("unexpected explicit gateway push url: %q", got)
	}
	if got := server.buildGatewayPushURL("https://gateway.example", "https://other.example/api/push/ticket-1?x=1"); got != "https://gateway.example/api/push/ticket-1?x=1" {
		t.Fatalf("unexpected absolute gateway push url: %q", got)
	}
}

func TestListAgentSummariesIncludesChatStats(t *testing.T) {
	server, chats, _ := newServerForHelperTests(t)
	runs := contracts.NewInMemoryRunManager()
	server.deps.Runs = runs
	server.deps.Registry = wsRegressionCatalogRegistry{
		items: []api.AgentSummary{
			{Key: "agent-a", Name: "Agent A"},
			{Key: "agent-b", Name: "Agent B"},
		},
	}

	if _, _, err := chats.EnsureChat("chat-a1", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a1: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-a2", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a2: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-b1", "agent-b", "", "hello"); err != nil {
		t.Fatalf("ensure chat-b1: %v", err)
	}
	if err := completeServerFixtureRun(t, chats, chat.RunCompletion{ChatID: "chat-a2", RunID: "loyw3v20", UpdatedAtMillis: testEpochMillis + 40_001}); err != nil {
		t.Fatalf("complete chat-a2: %v", err)
	}
	if _, err := chats.MarkRead("chat-a2", "loyw3v20"); err != nil {
		t.Fatalf("mark chat-a2 read: %v", err)
	}
	if err := completeServerFixtureRun(t, chats, chat.RunCompletion{
		ChatID:          "chat-a1",
		RunID:           "loyw3v28",
		UpdatedAtMillis: testEpochMillis + 40_003,
		Usage: chat.UsageData{
			PromptTokens:     7,
			CompletionTokens: 3,
			TotalTokens:      10,
		},
	}); err != nil {
		t.Fatalf("complete chat-a1: %v", err)
	}
	if err := completeServerFixtureRun(t, chats, chat.RunCompletion{ChatID: "chat-b1", RunID: "loyw3v2s", UpdatedAtMillis: testEpochMillis + 40_002}); err != nil {
		t.Fatalf("complete chat-b1: %v", err)
	}
	if _, err := chats.MarkRead("chat-b1", "loyw3v2s"); err != nil {
		t.Fatalf("mark chat-b1 read: %v", err)
	}
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-active-a1",
		ChatID:   "chat-a1",
		AgentKey: "agent-a",
	})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)

	items, err := server.listAgentSummaries(0, "")
	if err != nil {
		t.Fatalf("list agent summaries: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two agent summaries, got %#v", items)
	}
	statsByKey := map[string]api.AgentChatStats{}
	for _, item := range items {
		statsByKey[item.Key] = item.Stats
	}
	if got := statsByKey["agent-a"]; got.TotalCount != 2 || got.UnreadCount != 1 {
		t.Fatalf("unexpected agent-a stats: %#v", got)
	}
	if got := statsByKey["agent-b"]; got.TotalCount != 1 || got.UnreadCount != 0 {
		t.Fatalf("unexpected agent-b stats: %#v", got)
	}

	items, err = server.listAgentSummaries(1, "")
	if err != nil {
		t.Fatalf("list agent summaries with chats: %v", err)
	}
	chatsByKey := map[string][]api.ChatSummaryResponse{}
	for _, item := range items {
		chatsByKey[item.Key] = item.Chats
	}
	if got := chatsByKey["agent-a"]; len(got) != 1 || got[0].ChatID != "chat-a1" {
		t.Fatalf("unexpected agent-a chats: %#v", got)
	}
	if got := chatsByKey["agent-a"]; got[0].Usage != nil {
		t.Fatalf("agent chats should not include usage, got %#v", got[0].Usage)
	}
	if got := chatsByKey["agent-a"]; got[0].ActiveRun == nil ||
		got[0].ActiveRun.RunID != "run-active-a1" ||
		got[0].ActiveRun.State != string(contracts.RunLoopStateWaitingSubmit) ||
		got[0].ActiveRun.StartedAt == 0 {
		t.Fatalf("expected agent chat active run, got %#v", got[0].ActiveRun)
	}
	if got := chatsByKey["agent-a"]; got[0].ActiveRun.PlanningMode {
		t.Fatalf("agent chat active run should not include planningMode, got %#v", got[0].ActiveRun)
	}
	if got := chatsByKey["agent-b"]; len(got) != 1 || got[0].ChatID != "chat-b1" {
		t.Fatalf("unexpected agent-b chats: %#v", got)
	}
	if got := chatsByKey["agent-b"]; got[0].ActiveRun != nil {
		t.Fatalf("agent-b chat should not include activeRun, got %#v", got[0].ActiveRun)
	}

	chatSummaries, err := server.listChatSummaries("", "")
	if err != nil {
		t.Fatalf("list chat summaries: %v", err)
	}
	var chatA1 api.ChatSummaryResponse
	for _, item := range chatSummaries {
		if item.ChatID == "chat-a1" {
			chatA1 = item
			break
		}
	}
	if chatA1.ChatID == "" || chatA1.Usage == nil || chatA1.Usage.TotalTokens != 10 {
		t.Fatalf("/api/chats summaries should still include usage, got %#v", chatA1)
	}
	if chatA1.ActiveRun == nil ||
		chatA1.ActiveRun.RunID != "run-active-a1" ||
		chatA1.ActiveRun.State != string(contracts.RunLoopStateWaitingSubmit) ||
		chatA1.ActiveRun.StartedAt == 0 {
		t.Fatalf("/api/chats summaries should include activeRun, got %#v", chatA1.ActiveRun)
	}
	if chatA1.ActiveRun.PlanningMode {
		t.Fatalf("/api/chats summary active run should not include planningMode, got %#v", chatA1.ActiveRun)
	}
	var chatB1 api.ChatSummaryResponse
	for _, item := range chatSummaries {
		if item.ChatID == "chat-b1" {
			chatB1 = item
			break
		}
	}
	if chatB1.ChatID == "" || chatB1.ActiveRun != nil || chatB1.Error != nil {
		t.Fatalf("inactive /api/chats summary should omit activeRun and error, got %#v", chatB1)
	}

	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-active-a1-duplicate",
		ChatID:   "chat-a1",
		AgentKey: "agent-a",
	})
	items, err = server.listAgentSummaries(1, "")
	if err != nil {
		t.Fatalf("list agent summaries should keep chat-level active run conflicts: %v", err)
	}
	chatsByKey = map[string][]api.ChatSummaryResponse{}
	for _, item := range items {
		chatsByKey[item.Key] = item.Chats
	}
	got := chatsByKey["agent-a"]
	if len(got) != 1 || got[0].ChatID != "chat-a1" {
		t.Fatalf("unexpected agent-a chats after conflict: %#v", got)
	}
	if got[0].ActiveRun != nil {
		t.Fatalf("conflicted agent chat should not include activeRun, got %#v", got[0].ActiveRun)
	}
	if got[0].Error == nil {
		t.Fatalf("expected chat-level conflict error, got %#v", got[0])
	}
	assertActiveRunConflictInfo(t, *got[0].Error, activeRunConflictMessage, "chat-a1", "run-active-a1", "run-active-a1-duplicate")

	chatSummaries, err = server.listChatSummaries("", "")
	if err != nil {
		t.Fatalf("list chat summaries should keep chat-level active run conflicts: %v", err)
	}
	for _, item := range chatSummaries {
		if item.ChatID == "chat-a1" {
			chatA1 = item
			break
		}
	}
	if chatA1.ActiveRun != nil {
		t.Fatalf("conflicted /api/chats summary should not include activeRun, got %#v", chatA1.ActiveRun)
	}
	if chatA1.Error == nil {
		t.Fatalf("expected /api/chats summary conflict error, got %#v", chatA1)
	}
	assertActiveRunConflictInfo(t, *chatA1.Error, activeRunConflictMessage, "chat-a1", "run-active-a1", "run-active-a1-duplicate")
}

type wsRegressionCatalogRegistry struct {
	items []api.AgentSummary
}

func (r wsRegressionCatalogRegistry) Agents(string) []api.AgentSummary {
	return append([]api.AgentSummary(nil), r.items...)
}

func (wsRegressionCatalogRegistry) Teams() []api.TeamSummary { return nil }

func (wsRegressionCatalogRegistry) Skills(string) []api.SkillSummary { return nil }

func (wsRegressionCatalogRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}

func (wsRegressionCatalogRegistry) Tools(string, string) []api.ToolSummary { return nil }

func (wsRegressionCatalogRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}

func (wsRegressionCatalogRegistry) DefaultAgentKey() string { return "" }

func (wsRegressionCatalogRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if strings.TrimSpace(key) == "" {
		return catalog.AgentDefinition{}, false
	}
	return catalog.AgentDefinition{
		Key:           key,
		Name:          key,
		ModelKey:      "mock-model",
		MemoryEnabled: true,
	}, true
}

func (wsRegressionCatalogRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}

func (wsRegressionCatalogRegistry) Reload(context.Context, string) error { return nil }

func newServerForHelperTests(t *testing.T) (*Server, *chat.FileStore, *memory.FileStore) {
	t.Helper()
	root := t.TempDir()
	chats, err := chat.NewFileStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	server := &Server{
		deps: Dependencies{
			Config: config.Config{
				Memory: config.MemoryConfig{
					Enabled: true,
				},
			},
			Chats:    chats,
			Memory:   memories,
			Registry: wsRegressionCatalogRegistry{},
		},
		ticketService: NewResourceTicketService(config.ResourceTicketConfig{}),
	}
	return server, chats, memories
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, text string, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q in file contents", want)
	}
}

func assertNotContains(t *testing.T, text string, want string) {
	t.Helper()
	if strings.Contains(text, want) {
		t.Fatalf("did not expect %q in file contents", want)
	}
}
