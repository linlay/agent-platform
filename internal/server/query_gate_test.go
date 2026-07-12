package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func TestQueryGateRejectsPendingAwaitingModes(t *testing.T) {
	fixture := newTestFixture(t)
	nowMs := time.Now().UnixMilli()
	cases := []struct {
		mode       string
		awaitingID string
		ask        map[string]any
	}{
		{
			mode:       "question",
			awaitingID: "await-question",
			ask: map[string]any{
				"questions": []any{map[string]any{"id": "q1", "question": "Continue?", "type": "text"}},
			},
		},
		{
			mode:       "planning",
			awaitingID: "await-planning",
			ask: map[string]any{
				"planning": map[string]any{"id": "confirm-planning", "planningId": "planning-1"},
			},
		},
		{
			mode:       "form",
			awaitingID: "await-form",
			ask: map[string]any{
				"forms": []any{map[string]any{"id": "form-1", "command": "mock form", "form": map[string]any{"days": 1}}},
			},
		},
		{
			mode:       "approval",
			awaitingID: "await-approval",
			ask: map[string]any{
				"approvals": []any{map[string]any{"id": "cmd-1", "command": "git push origin main"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			chatID := "chat-gate-" + tc.mode
			runID := "run-gate-" + tc.mode
			seedDeferredAwaitingPayload(t, fixture.chats, chatID, runID, tc.awaitingID, tc.mode, 600, nowMs, tc.ask)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"second query"}`))
			req.Header.Set("Content-Type", "application/json")
			fixture.server.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp api.ApiResponse[api.ChatErrorInfo]
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Msg != awaitingPendingCode || resp.Data.Code != awaitingPendingCode {
				t.Fatalf("expected awaiting_pending response, got %#v", resp)
			}
			if resp.Data.ChatID != chatID || resp.Data.Awaiting == nil {
				t.Fatalf("expected awaiting payload for chat, got %#v", resp.Data)
			}
			if resp.Data.Awaiting.AwaitingID != tc.awaitingID || resp.Data.Awaiting.RunID != runID || resp.Data.Awaiting.Mode != tc.mode || resp.Data.Awaiting.Status != "awaiting" {
				t.Fatalf("unexpected awaiting payload %#v", resp.Data.Awaiting)
			}
		})
	}
}

func TestQueryGateCleansInvalidPendingAwaitingAndAllowsQuery(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, store chat.Store, chatID string)
	}{
		{
			name: "dangling",
			seed: func(t *testing.T, store chat.Store, chatID string) {
				t.Helper()
				if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
					t.Fatalf("ensure chat: %v", err)
				}
				if err := store.SetPendingAwaiting(chatID, chat.PendingAwaiting{
					AwaitingID: "await-dangling",
					RunID:      "run-dangling",
					Mode:       "question",
					CreatedAt:  time.Now().UnixMilli(),
				}); err != nil {
					t.Fatalf("set pending awaiting: %v", err)
				}
			},
		},
		{
			name: "invalid-mode",
			seed: func(t *testing.T, store chat.Store, chatID string) {
				t.Helper()
				if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
					t.Fatalf("ensure chat: %v", err)
				}
				if err := store.SetPendingAwaiting(chatID, chat.PendingAwaiting{
					AwaitingID: "await-invalid",
					RunID:      "run-invalid",
					Mode:       "other",
					CreatedAt:  time.Now().UnixMilli(),
				}); err != nil {
					t.Fatalf("set pending awaiting: %v", err)
				}
			},
		},
		{
			name: "expired",
			seed: func(t *testing.T, store chat.Store, chatID string) {
				t.Helper()
				seedDeferredAwaiting(t, store, chatID, "run-expired-gate", "await-expired-gate", "question", 1, time.Now().UnixMilli()-2000)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestFixture(t)
			chatID := "chat-gate-clean-" + tc.name
			tc.seed(t, fixture.chats, chatID)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"allowed query"}`))
			req.Header.Set("Content-Type", "application/json")
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected query to be allowed, got %d: %s", rec.Code, rec.Body.String())
			}
			summary, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatalf("load summary: %v", err)
			}
			if summary == nil || summary.PendingAwaiting != nil {
				t.Fatalf("expected pending awaiting cleared, got %#v", summary)
			}
		})
	}
}

func TestQueryRejectsExistingLiveActiveRun(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-live-active"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	_, _, _ = fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-live-active",
		ChatID:   chatID,
		AgentKey: "mock-agent",
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"second query"}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.ChatErrorInfo]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Msg != activeRunConflictCode || resp.Data.Code != activeRunConflictCode || len(resp.Data.RunIDs) != 1 || resp.Data.RunIDs[0] != "run-live-active" {
		t.Fatalf("unexpected active run conflict response %#v", resp)
	}
}
