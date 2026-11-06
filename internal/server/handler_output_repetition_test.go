package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/chat"
)

func TestQueryOutputRepetitionCancelsUpstreamAndExcludesFailedHistory(t *testing.T) {
	const chatID = "repetition-cancel"
	var calls atomic.Int32
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		quoted, _ := json.Marshal(strings.Repeat("repeat-loop-sentinel ", 250))
		writeProviderSSE(t, w, `{"choices":[{"delta":{"reasoning_content":`+string(quoted)+`}}]}`)
		select {
		case <-r.Context().Done():
			cancelled <- struct{}{}
		case <-release:
		}
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"hello"}`)))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not terminate after repeated provider output")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider request context was not cancelled")
	}
	body := recorder.Body.String()
	if calls.Load() != 1 || !strings.Contains(body, `"code":"model_output_repetition"`) || !strings.Contains(body, `"type":"run.error"`) || strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("calls=%d unexpected stream: %s", calls.Load(), body)
	}
	raw, err := fixture.chats.LoadRawMessages(chatID, chat.DefaultHistoryRunWindow)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	if strings.Contains(string(encoded), "repeat-loop-sentinel") {
		t.Fatal("discarded model output entered continuation history")
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range detail.Events {
		payload, _ := json.Marshal(event.Payload)
		if event.Type == "run.error" && strings.Contains(string(payload), "model_output_repetition") {
			return
		}
	}
	t.Fatal("repetition error was not persisted")
}
