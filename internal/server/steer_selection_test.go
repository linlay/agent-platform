package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/ws"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSelectionSteerNonVisionPersistsAndReplays(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		t.Run(transport, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			requests := make(chan map[string]any, 4)
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				requests <- payload
				if calls.Add(1) == 1 {
					writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"draft"}}]}`)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{notifications: ws.NewHub()})
			server := newLoopbackServer(t, fixture.server)
			defer server.Close()
			defer unblock()
			const chatID = "selection-steer-chat"
			runID, readTail := startSteerTransportRun(t, server.URL, transport, chatID)
			payload := api.SteerRequest{RunID: runID, ChatID: chatID, AgentKey: "mock-agent", SteerID: "selection-1", Message: "explain this", References: []api.Reference{{Type: "selection", Meta: map[string]any{"text": "UNIQUE_SELECTED_TEXT"}}}}
			var raw []byte
			if transport == "ws" {
				raw = wsTestControlResponse(t, server.URL, "/api/steer", payload)
			} else {
				body, _ := json.Marshal(payload)
				resp, err := http.Post(server.URL+"/api/steer", "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				raw, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			var ack struct {
				Data api.SteerResponse `json:"data"`
			}
			if err := json.Unmarshal(raw, &ack); err != nil || !ack.Data.Accepted {
				t.Fatalf("steer: %s %v", raw, err)
			}
			unblock()
			tail, err := readTail()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(tail, []byte(`"type":"request.steer"`)) {
				t.Fatalf("missing steer event: %s", tail)
			}
			<-requests
			assertSelection := func(payload map[string]any) {
				t.Helper()
				data, _ := json.Marshal(payload)
				if bytes.Count(data, []byte("UNIQUE_SELECTED_TEXT")) != 1 {
					t.Fatalf("model input: %s", data)
				}
			}
			assertSelection(<-requests)
			jsonl, err := fixture.chats.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(jsonl, `"_type":"steer"`) != 1 {
				t.Fatalf("steer must persist once: %s", jsonl)
			}
			replay, err := fixture.chats.LoadChat(chatID)
			if err != nil {
				t.Fatal(err)
			}
			replayJSON, _ := json.Marshal(replay)
			if !bytes.Contains(replayJSON, []byte("UNIQUE_SELECTED_TEXT")) {
				t.Fatalf("selection missing in replay: %s", replayJSON)
			}
			next, err := http.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"continue"}`))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, next.Body)
			next.Body.Close()
			assertSelection(<-requests)
		})
	}
}
