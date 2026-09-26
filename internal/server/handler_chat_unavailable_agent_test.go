package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

// Catalog membership is execution metadata, not the authority for persisted
// Chat existence or replay. No provider call is needed for these regressions.
func TestChatHistorySurvivesUnavailableAgent(t *testing.T) {
	for _, state := range []string{"valid", "invalid", "deleted"} {
		t.Run(state, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("history reads and rejected continuation must not call the model")
				w.WriteHeader(http.StatusInternalServerError)
			})
			store := fixture.chats.(*chat.FileStore)
			const chatID = "historical-chat"
			seedAgentModeChat(t, store, chatID, "historical-run", "mock-agent", "", "REACT", 1000)
			at := int64(1_700_000_001_000)
			if err := store.AppendStepLine(chatID, chat.StepLine{
				Type: chat.StepLineTypeReact, ChatID: chatID, RunID: "historical-run", Seq: 1, UpdatedAt: at,
				Messages: []chat.StoredMessage{{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: "persisted answer"}}, Ts: &at}},
			}); err != nil {
				t.Fatal(err)
			}
			read := func(path string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				return rec
			}
			before := read("/api/chat?chatId=" + chatID + "&includeRawMessages=true")
			if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "persisted answer") {
				t.Fatalf("initial history: %d %s", before.Code, before.Body.String())
			}
			agentDir := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent")
			switch state {
			case "invalid":
				// The legacy Desktop configuration is a real catalog failure.
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: mock-agent\nname: Broken\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\ntoolConfig:\n  tools:\n    - desktop_action\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := os.RemoveAll(agentDir); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.registry.(*catalog.FileRegistry).Reload(context.Background(), "history regression"); err != nil {
				t.Fatal(err)
			}
			wantStatus := http.StatusNotFound
			if state == "valid" {
				wantStatus = http.StatusOK
			}
			if got := read("/api/agent?agentKey=mock-agent"); got.Code != wantStatus {
				t.Fatalf("Agent status: %d %s", got.Code, got.Body.String())
			}
			for _, path := range []string{"/api/chats", "/api/chats?agentKey=mock-agent&mode=REACT"} {
				got := read(path)
				var response api.ApiResponse[[]api.ChatSummaryResponse]
				if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil || got.Code != http.StatusOK || len(response.Data) != 1 || response.Data[0].ChatID != chatID {
					t.Fatalf("history discovery %s: %d %s (%v)", path, got.Code, got.Body.String(), err)
				}
			}
			if state != "valid" {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"historical-chat","agentKey":"mock-agent","message":"continue"}`)))
				if rec.Code < 400 {
					t.Fatalf("unavailable Agent must reject continuation: %d %s", rec.Code, rec.Body.String())
				}
			}
			after := read("/api/chat?chatId=" + chatID + "&includeRawMessages=true")
			if after.Code != http.StatusOK || after.Body.String() != before.Body.String() {
				t.Fatalf("Agent %s changed persisted replay: before=%s after=%s", state, before.Body.String(), after.Body.String())
			}
			if got := read("/api/chat?chatId=missing-chat"); got.Code != http.StatusNotFound {
				t.Fatalf("missing Chat must remain 404: %d %s", got.Code, got.Body.String())
			}

			// Losing the Agent must not turn history reads into an auth bypass.
			privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
			fixture.cfg.Auth = config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicKeyPath, Issuer: "history-test"}
			fixture.server = newServerFromFixture(t, fixture)
			for _, path := range []string{"/api/chats", "/api/chat?chatId=" + chatID} {
				if got := read(path); got.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated history: %d %s", got.Code, got.Body.String())
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{"sub": "tester", "iss": "history-test", "exp": float64(4102444800)}))
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("authenticated history: %d %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
