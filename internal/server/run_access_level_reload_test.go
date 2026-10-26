package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
)

func TestActiveChatReloadKeepsUpdatedAccessLevel(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	req := httptest.NewRequest(http.MethodPost, "/api/query?detail=full", bytes.NewBufferString(`{"chatId":"chat-access-reload","agentKey":"mock-agent","message":"hello","accessLevel":"default"}`))
	prepared, err := prepareQueryForTest(fixture.server, req)
	if err != nil {
		t.Fatal(err)
	}
	registered, statusErr := fixture.server.registerQueryRun(context.Background(), prepared)
	if statusErr != nil {
		t.Fatalf("register: %#v", statusErr)
	}
	defer fixture.server.finishRegisteredQueryRun(prepared, registered)
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()

	for i, level := range []string{contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess, contracts.AccessLevelDefault} {
		ack := fixture.runs.UpdateAccessLevel(api.AccessLevelRequest{RunID: prepared.Req.RunID, AgentKey: "mock-agent", AccessLevel: level})
		if !ack.Accepted {
			t.Fatalf("update %s: %#v", level, ack)
		}
		// A new detail read models returning to the Chat after its live observer
		// was detached. The persisted query still contains the initial default.
		for reload := 0; reload < 2; reload++ {
			var body []byte
			if reload == 0 {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?detail=full&chatId="+prepared.Req.ChatID+"&includeRawMessages=false", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("reload status=%d body=%s", rec.Code, rec.Body.String())
				}
				body = rec.Body.Bytes()
			} else {
				body = wsTestControlResponse(t, httpServer.URL, "/api/chat", map[string]any{"chatId": prepared.Req.ChatID, "includeRawMessages": false})
			}
			var response struct {
				Data struct {
					ActiveRun struct {
						AccessLevel string `json:"accessLevel"`
						Version     int64  `json:"accessLevelVersion"`
					} `json:"activeRun"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.ActiveRun.AccessLevel != level || response.Data.ActiveRun.Version != int64(i+2) {
				t.Fatalf("reload lost current permission: want=%s version=%d got=%+v", level, i+2, response.Data.ActiveRun)
			}
		}
	}
}
