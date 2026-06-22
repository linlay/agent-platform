package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestHandleChatArchiveArchivesChatAndBroadcasts(t *testing.T) {
	server, active, _ := newArchiveHandlerTestServer(t, nil)
	seedArchiveHandlerChat(t, active, "chat-http-archive")

	body := bytes.NewBufferString(`{"chatIds":["chat-http-archive"]}`)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.ArchiveChatResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data.Results) != 1 || !resp.Data.Results[0].Success {
		t.Fatalf("unexpected archive response: %#v", resp.Data)
	}
	if sum, err := active.Summary("chat-http-archive"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum != nil {
		t.Fatalf("expected active chat removed")
	}

	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/archives?agentKey=agent-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("archives status=%d body=%s", rec.Code, rec.Body.String())
	}
	var archives api.ApiResponse[api.ArchivesResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &archives); err != nil {
		t.Fatalf("decode archives: %v", err)
	}
	if archives.Data.Total != 1 || archives.Data.Items[0].ChatID != "chat-http-archive" {
		t.Fatalf("unexpected archives response: %#v", archives.Data)
	}
	archivedSummary := archives.Data.Items[0]
	if archivedSummary.CreatedAt <= 0 || archivedSummary.LastRunAt != 2000 || archivedSummary.ArchivedAt <= 0 {
		t.Fatalf("expected archive timestamps, got %#v", archivedSummary)
	}
	usage := archivedSummary.Usage
	if usage == nil || usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CacheHitTokens != 2 ||
		usage.PromptTokensDetails.CacheMissTokens != 1 ||
		usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 4 ||
		usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed archive usage, got %#v", usage)
	}

	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/archive?chatId=chat-http-archive", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("archive detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail api.ApiResponse[api.ArchivedChatDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode archive detail: %v", err)
	}
	if detail.Data.CreatedAt != archivedSummary.CreatedAt || detail.Data.LastRunAt != 2000 || detail.Data.ArchivedAt != archivedSummary.ArchivedAt {
		t.Fatalf("unexpected archive detail timestamps: %#v", detail.Data)
	}
}

func TestHandleChatArchiveReportsActiveRunConflictPerItem(t *testing.T) {
	runs := &archiveHandlerRunManager{activeChatID: "chat-active"}
	server, active, _ := newArchiveHandlerTestServer(t, runs)
	seedArchiveHandlerChat(t, active, "chat-active")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", bytes.NewBufferString(`{"chatIds":["chat-active"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.ArchiveChatResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data.Results) != 1 || resp.Data.Results[0].Success || resp.Data.Results[0].Error != "active run conflict" {
		t.Fatalf("unexpected archive response: %#v", resp.Data)
	}
	if sum, err := active.Summary("chat-active"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum == nil {
		t.Fatalf("expected active chat to remain")
	}
}

func TestHandleArchiveRestoreRestoresChat(t *testing.T) {
	server, active, archiveStore := newArchiveHandlerTestServer(t, nil)
	seedArchiveHandlerChat(t, active, "chat-http-restore")

	archiveRec := httptest.NewRecorder()
	server.ServeHTTP(archiveRec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", bytes.NewBufferString(`{"chatIds":["chat-http-restore"]}`)))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/archive/restore", bytes.NewBufferString(`{"chatIds":["chat-http-restore"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.ArchiveRestoreResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if len(resp.Data.Results) != 1 || !resp.Data.Results[0].Success || resp.Data.Results[0].Summary == nil {
		t.Fatalf("unexpected restore response: %#v", resp.Data)
	}
	if resp.Data.Results[0].Summary.ChatID != "chat-http-restore" || resp.Data.Results[0].Summary.AgentKey != "agent-a" {
		t.Fatalf("unexpected restored summary: %#v", resp.Data.Results[0].Summary)
	}
	if detail, err := active.LoadChat("chat-http-restore"); err != nil {
		t.Fatalf("load restored chat: %v", err)
	} else if len(detail.Events) == 0 {
		t.Fatalf("expected restored events")
	}
	if _, err := archiveStore.LoadArchived("chat-http-restore"); !errors.Is(err, chat.ErrChatNotFound) {
		t.Fatalf("expected archive removed, got %v", err)
	}
}

func TestHandleArchiveRestoreReportsActiveConflictPerItem(t *testing.T) {
	server, active, archiveStore := newArchiveHandlerTestServer(t, nil)
	if err := archiveStore.ArchiveChat(chat.ArchivedChat{
		Summary: chat.ArchivedSummary{
			ChatID:         "chat-http-restore-conflict",
			ChatName:       "Archived conflict",
			AgentKey:       "agent-a",
			CreatedAt:      1000,
			UpdatedAt:      2000,
			ArchivedAt:     3000,
			LastRunID:      "run-restore-conflict",
			LastRunContent: "archived",
		},
		JSONLContent: `{"chatId":"chat-http-restore-conflict","runId":"run-restore-conflict","updatedAt":1000,"query":{"message":"hello"},"_type":"query"}` + "\n",
	}); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := active.EnsureChat("chat-http-restore-conflict", "agent-a", "", "active"); err != nil {
		t.Fatalf("ensure active: %v", err)
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/archive/restore", bytes.NewBufferString(`{"chatIds":["chat-http-restore-conflict"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.ArchiveRestoreResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if len(resp.Data.Results) != 1 || resp.Data.Results[0].Success || resp.Data.Results[0].Error != "active chat already exists" {
		t.Fatalf("unexpected restore response: %#v", resp.Data)
	}
	if _, err := archiveStore.LoadArchived("chat-http-restore-conflict"); err != nil {
		t.Fatalf("archive should remain after restore conflict: %v", err)
	}
}

func newArchiveHandlerTestServer(t *testing.T, runs contracts.RunManager) (*Server, *chat.FileStore, *chat.ArchiveStore) {
	t.Helper()
	root := t.TempDir()
	active, err := chat.NewFileStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archiveStore, err := chat.NewArchiveStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	server, err := New(Dependencies{
		Config:   config.Config{},
		Chats:    active,
		Archives: archiveStore,
		Archiver: chat.NewArchiver(active, archiveStore),
		Runs:     runs,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server, active, archiveStore
}

func seedArchiveHandlerChat(t *testing.T, store *chat.FileStore, chatID string) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     "run-" + chatID,
		UpdatedAt: 1000,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           "run-" + chatID,
		AgentKey:        "agent-a",
		AssistantText:   "archived response",
		InitialMessage:  "hello",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
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
		t.Fatalf("complete run: %v", err)
	}
}

type archiveHandlerRunManager struct {
	activeChatID string
}

func (m *archiveHandlerRunManager) ActiveRunForChat(chatID string) (contracts.RunStatusInfo, bool, error) {
	if chatID == m.activeChatID {
		return contracts.RunStatusInfo{RunID: "run-active"}, true, nil
	}
	return contracts.RunStatusInfo{}, false, nil
}

func (m *archiveHandlerRunManager) Register(ctx context.Context, _ contracts.QuerySession) (context.Context, *contracts.RunControl, contracts.ActiveRun) {
	return ctx, nil, contracts.ActiveRun{}
}
func (m *archiveHandlerRunManager) LookupAwaiting(string, string) (contracts.AwaitingSubmitContext, bool) {
	return contracts.AwaitingSubmitContext{}, false
}
func (m *archiveHandlerRunManager) Submit(api.SubmitRequest) contracts.SubmitAck {
	return contracts.SubmitAck{}
}
func (m *archiveHandlerRunManager) Steer(api.SteerRequest) contracts.SteerAck {
	return contracts.SteerAck{}
}
func (m *archiveHandlerRunManager) Interrupt(api.InterruptRequest) contracts.InterruptAck {
	return contracts.InterruptAck{}
}
func (m *archiveHandlerRunManager) UpdateAccessLevel(api.AccessLevelRequest) contracts.AccessLevelAck {
	return contracts.AccessLevelAck{}
}
func (m *archiveHandlerRunManager) Finish(string) {}
func (m *archiveHandlerRunManager) AttachObserver(string, int64) (*stream.Observer, error) {
	return nil, nil
}
func (m *archiveHandlerRunManager) DetachObserver(string, string) {}
func (m *archiveHandlerRunManager) EventBus(string) (*stream.RunEventBus, bool) {
	return nil, false
}
func (m *archiveHandlerRunManager) RunStatus(string) (contracts.RunStatusInfo, bool) {
	return contracts.RunStatusInfo{}, false
}
