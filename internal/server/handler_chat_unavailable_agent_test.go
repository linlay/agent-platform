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
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/i18n"
)

// Catalog membership is execution metadata, not the authority for persisted
// Chat existence or replay. No provider call is needed for these regressions.
func TestChatHistorySurvivesUnavailableAgent(t *testing.T) {
	for _, state := range []string{"valid", "invalid", "legacy-mode", "deleted"} {
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
				// A missing declared connector remains a real catalog failure.
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: mock-agent\nname: Broken\nmode: GENERAL\nmodelConfig:\n  modelKey: mock-model\nconnectorConfig:\n  connectors:\n    - missing.connector\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "legacy-mode":
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: mock-agent\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n"), 0o644); err != nil {
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
			if state != "deleted" {
				wantStatus = http.StatusOK
			}
			if got := read("/api/agent?agentKey=mock-agent"); got.Code != wantStatus {
				t.Fatalf("Agent status: %d %s", got.Code, got.Body.String())
			}
			for _, path := range []string{"/api/chats", "/api/chats?agentKey=mock-agent&mode=GENERAL"} {
				got := read(path)
				var response api.ApiResponse[[]api.ChatSummaryResponse]
				if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil || got.Code != http.StatusOK || len(response.Data) != 1 || response.Data[0].ChatID != chatID {
					t.Fatalf("history discovery %s: %d %s (%v)", path, got.Code, got.Body.String(), err)
				}
			}
			if state == "deleted" {
				code, status := apperrors.CodeAgentNotFound, http.StatusNotFound
				if state == "invalid" || state == "legacy-mode" {
					code, status = apperrors.CodeAgentConfigurationInvalid, http.StatusUnprocessableEntity
				}
				for _, locale := range []string{i18n.LocaleEN, i18n.LocaleZhCN} {
					for _, stream := range []bool{true, false} {
						body, err := json.Marshal(map[string]any{"chatId": chatID, "agentKey": "mock-agent", "message": "continue", "stream": stream})
						if err != nil {
							t.Fatal(err)
						}
						req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body))
						req.Header.Set("X-Locale", locale)
						rec := httptest.NewRecorder()
						fixture.server.ServeHTTP(rec, req)
						var response api.ApiResponse[map[string]any]
						if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						payload, _ := response.Data["error"].(map[string]any)
						if rec.Code != status || response.Code != status || payload["code"] != string(code) || payload["status"] != float64(status) || payload["retryable"] != false {
							t.Fatalf("locale=%s stream=%v: unexpected error: %d %s", locale, stream, rec.Code, rec.Body.String())
						}
						if message, _ := payload["message"].(string); message == "" || response.Msg != message || i18n.Translate(locale, string(code), message) != message {
							t.Fatalf("locale=%s: inconsistent localized message: %s", locale, rec.Body.String())
						}
					}
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
