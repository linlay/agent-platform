package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	platformws "agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type detachedQueryFrame struct {
	Frame string `json:"frame"`
	ID    string `json:"id"`
	Type  string `json:"type"`
	Code  int    `json:"code"`
	Data  struct {
		Accepted  bool   `json:"accepted"`
		Status    string `json:"status"`
		RunID     string `json:"runId"`
		ChatID    string `json:"chatId"`
		AgentKey  string `json:"agentKey"`
		StartedAt int64  `json:"startedAt"`
	} `json:"data"`
	Event json.RawMessage `json:"event"`
}

func TestDetachedQueryStartsRunWithoutOccupyingTheStreamSlot(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"running"}}]}`)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{notifications: platformws.NewHub()})
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	defer unblock()
	dial := func(lane string) *gws.Conn {
		t.Helper()
		c, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?source=desktop-"+lane+"&surfaceId=desktop-"+lane+"&deviceId=detached-device", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		readConnectedPush(t, c)
		return c
	}
	// Returns the first frame for id and fails on any stream frame of a detached request.
	readID := func(c *gws.Conn, id string, detached map[string]bool) detachedQueryFrame {
		t.Helper()
		for {
			if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var f detachedQueryFrame
			if err := c.ReadJSON(&f); err != nil {
				t.Fatalf("read %s: %v", id, err)
			}
			if f.Frame == "stream" && detached[f.ID] {
				t.Fatalf("detached query produced a stream frame: %#v", f)
			}
			if f.ID == id {
				return f
			}
		}
	}
	main := dial("main")
	detached := map[string]bool{"bg-1": true, "bg-2": true}

	// A foreground stream occupies the connection's single Run stream slot.
	sendSelectionLaneRequest(t, main, "fg", "/api/query", map[string]any{"chatId": "detached-fg", "agentKey": "mock-agent", "message": "foreground"})
	if f := readID(main, "fg", detached); f.Frame != "stream" {
		t.Fatalf("foreground start: %#v", f)
	}

	// Detached queries are accepted while that stream is live, and do not queue behind each other.
	runs := map[string]string{}
	for _, id := range []string{"bg-1", "bg-2"} {
		chatID := "detached-" + id
		sendSelectionLaneRequest(t, main, id, "/api/query", map[string]any{"chatId": chatID, "runId": "run-" + id, "agentKey": "mock-agent", "message": id, "detached": true})
		f := readID(main, id, detached)
		if f.Frame != "response" || !f.Data.Accepted || f.Data.Status != "running" || f.Data.RunID != "run-"+id ||
			f.Data.ChatID != chatID || f.Data.AgentKey != "mock-agent" || f.Data.StartedAt <= 0 {
			t.Fatalf("detached start %s: %#v", id, f)
		}
		runs[id] = f.Data.RunID
	}

	// The slot is still held by the foreground stream only.
	sendSelectionLaneRequest(t, main, "second", "/api/query", map[string]any{"chatId": "must-not-exist", "agentKey": "mock-agent", "message": "second"})
	if f := readID(main, "second", detached); f.Type != "active_stream_exists" {
		t.Fatalf("second stream: %#v", f)
	}

	// The starting connection keeps control of its background Run.
	sendSelectionLaneRequest(t, main, "interrupt-bg", "/api/interrupt", map[string]any{"runId": runs["bg-2"], "agentKey": "mock-agent", "message": "stop"})
	if f := readID(main, "interrupt-bg", detached); f.Frame != "response" || !f.Data.Accepted {
		t.Fatalf("interrupt detached run: %#v", f)
	}

	// Side lanes and HTTP do not accept the detached mode.
	btw := dial("btw")
	sendSelectionLaneRequest(t, btw, "side", "/api/query", map[string]any{"chatId": "detached-fg", "message": "side", "detached": true})
	if f := readID(btw, "side", nil); f.Frame != "error" || f.Code != http.StatusBadRequest {
		t.Fatalf("side detached: %#v", f)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"chatId":"detached-http","agentKey":"mock-agent","message":"http","detached":true}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detached_ws_required") {
		t.Fatalf("HTTP detached: %d %s", rec.Code, rec.Body.String())
	}
	if summary, err := fixture.chats.Summary("detached-http"); err == nil && summary != nil {
		t.Fatal("rejected HTTP detached query created chat")
	}
}
