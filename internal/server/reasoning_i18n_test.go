package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/conversationexport"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
)

func TestReasoningLocaleMatchesSSEHistoryAndExport(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"reasoning_content":"原始推理内容"}}]}`,
			`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	for _, locale := range []string{"en", "zh-cn"} {
		req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"think","agentKey":"mock-agent"}`))
		req.Header.Set("X-Locale", locale)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
		}
		var chatID, reasoningID string
		for _, event := range decodeSSEMessages(t, rec.Body.String()) {
			if event["type"] == "chat.start" {
				chatID, _ = event["chatId"].(string)
			}
			if event["type"] == "reasoning.start" {
				reasoningID, _ = event["reasoningId"].(string)
				if event["reasoningLabel"] != i18n.ReasoningLabelForID(locale, reasoningID) {
					t.Fatalf("%s SSE: %#v", locale, event)
				}
			}
		}
		if chatID == "" || reasoningID == "" {
			t.Fatalf("missing Chat/reasoning in SSE: %s", rec.Body.String())
		}
		original, err := fixture.chats.LoadJSONLContent(chatID)
		if err != nil {
			t.Fatal(err)
		}
		for _, viewer := range []struct{ locale, snapshotLocale string }{{"zh-cn", "zh-CN"}, {"en", "en-US"}} {
			req = httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil)
			req.Header.Set("X-Locale", viewer.locale)
			rec = httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			var history api.ApiResponse[struct {
				Events []stream.EventData `json:"events"`
			}]
			if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("history: %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			found := false
			for _, event := range history.Data.Events {
				if event.Type == "reasoning.snapshot" {
					found = true
					if event.String("reasoningId") != reasoningID || event.String("reasoningLabel") != i18n.ReasoningLabelForID(viewer.locale, reasoningID) || event.String("text") != "原始推理内容" {
						t.Fatalf("%s history: %#v", viewer.locale, event)
					}
				}
			}
			if !found {
				t.Fatal("missing reasoning in history")
			}
			req = httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId="+chatID+"&format=snapshot", nil)
			req.Header.Set("X-Locale", viewer.locale)
			rec = httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			var snapshot conversationexport.SnapshotV1
			if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("export: %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			if snapshot.Locale != viewer.snapshotLocale || len(snapshot.Turns) != 1 {
				t.Fatalf("export locale/turns: %+v", snapshot)
			}
			found = false
			for _, node := range snapshot.Turns[0].Nodes {
				if node.Kind == "thinking" {
					found = true
					if node.ReasoningLabel != i18n.ReasoningLabelForID(viewer.locale, reasoningID) || node.Text != "原始推理内容" {
						t.Fatalf("%s export: %+v", viewer.locale, node)
					}
				}
			}
			if !found {
				t.Fatal("missing reasoning in export")
			}
		}
		if after, err := fixture.chats.LoadJSONLContent(chatID); err != nil || after != original {
			t.Fatalf("reading localized history/export changed stored JSONL: %v", err)
		}
	}
}
