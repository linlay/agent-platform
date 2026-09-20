package server

import (
	"agent-platform/internal/chat"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agent-platform/internal/runtime/controlscope"
	platformws "agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestDesktopLanesRunInParallelAndRejectCrossControls(t *testing.T) {
	var blocked atomic.Bool
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if blocked.Load() {
			calls.Add(1)
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"running"}}]}`)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{notifications: platformws.NewHub()})
	const parent = "lane-control-parent"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+parent+`","agentKey":"mock-agent","message":"parent","stream":false}`)
	parentSummary, _ := fixture.chats.Summary(parent)
	parentJSONL, _ := fixture.chats.LoadJSONLContent(parent)
	blocked.Store(true)
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	defer unblock()
	dial := func(lane, device string) *gws.Conn {
		t.Helper()
		c, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?source=desktop-"+lane+"&surfaceId=desktop-"+lane+"&deviceId="+device, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		readConnectedPush(t, c)
		return c
	}
	readID := func(c *gws.Conn, id string) selectionLaneFrame {
		t.Helper()
		for {
			f := readSelectionLaneFrame(t, c)
			if f.ID == id {
				return f
			}
		}
	}
	lanes := []string{"main", "btw", "explain"}
	conns := map[string]*gws.Conn{}
	runs := map[string]string{}
	for _, lane := range lanes {
		c := dial(lane, "lane-device")
		conns[lane] = c
		chatID := parent
		if lane == "main" {
			chatID = "lane-control-main"
		}
		sendSelectionLaneRequest(t, c, "start-"+lane, "/api/query", map[string]any{"chatId": chatID, "agentKey": "mock-agent", "message": lane})
		for runs[lane] == "" {
			f := readID(c, "start-"+lane)
			if f.Frame == "error" {
				t.Fatalf("start: %#v", f)
			}
			if f.Event != nil && f.Event.Type == "run.start" {
				runs[lane], _ = f.Event.Value("runId").(string)
			}
		}
	}
	routes := []string{"/api/attach", "/api/detach", "/api/steer", "/api/interrupt", "/api/access-level"}
	for _, from := range lanes {
		for _, to := range lanes {
			if from == to {
				continue
			}
			for i, route := range routes {
				id := fmt.Sprintf("cross-%s-%s-%d", from, to, i)
				sendSelectionLaneRequest(t, conns[from], id, route, map[string]any{"runId": runs[to], "agentKey": "mock-agent", "message": "change", "awaitingId": "unknown", "accessLevel": "full_access"})
				f := readID(conns[from], id)
				if f.Frame != "error" || f.Code != 403 || f.Type != "run_lane_mismatch" {
					t.Fatalf("%s %s -> %s: %#v", route, from, to, f)
				}
			}
		}
	}
	// A second query must fail before chat creation or provider execution.
	sendSelectionLaneRequest(t, conns["main"], "second", "/api/query", map[string]any{"chatId": "must-not-exist", "agentKey": "mock-agent", "message": "second"})
	if f := readID(conns["main"], "second"); f.Type != "active_stream_exists" {
		t.Fatalf("second stream: %#v", f)
	}
	if summary, err := fixture.chats.Summary("must-not-exist"); err == nil && summary != nil {
		t.Fatal("rejected query created chat")
	}
	// HTTP cannot control any WS run, including via payload lane spoofing.
	for _, lane := range lanes {
		for _, route := range routes {
			if route == "/api/detach" {
				continue
			}
			method, path := http.MethodPost, route
			body := marshalPayload(map[string]any{"runId": runs[lane], "agentKey": "mock-agent", "message": "change", "awaitingId": "unknown", "accessLevel": "full_access", "lane": lane})
			if route == "/api/attach" {
				method = http.MethodGet
				path += "?runId=" + runs[lane] + "&agentKey=mock-agent"
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(body)))
			if rec.Code != 403 || !strings.Contains(rec.Body.String(), "run_transport_mismatch") {
				t.Fatalf("HTTP %s: %d %s", route, rec.Code, rec.Body.String())
			}
		}
	}
	// WS cannot control the existing HTTP parent run.
	for i, route := range routes {
		id := fmt.Sprintf("http-owner-%d", i)
		sendSelectionLaneRequest(t, conns["main"], id, route, map[string]any{"runId": parentSummary.LastRunID, "agentKey": "mock-agent", "message": "change", "awaitingId": "unknown", "accessLevel": "full_access"})
		if f := readID(conns["main"], id); f.Code != 403 || f.Type != "run_transport_mismatch" {
			t.Fatalf("WS -> HTTP %s: %#v", route, f)
		}
	}
	// Closing and reopening each lane preserves control ownership and the Run.
	for _, lane := range lanes {
		c := conns[lane]
		sendSelectionLaneRequest(t, c, "detach", "/api/detach", map[string]any{"runId": runs[lane], "agentKey": "mock-agent"})
		if f := readID(c, "detach"); !f.Data.Accepted {
			t.Fatalf("detach: %#v", f)
		}
		c.Close()
		c = dial(lane, "lane-device")
		conns[lane] = c
		sendSelectionLaneRequest(t, c, "restore", "/api/attach", map[string]any{"runId": runs[lane], "agentKey": "mock-agent", "lastSeq": 0})
		if f := readID(c, "restore"); f.Frame != "stream" {
			t.Fatalf("restore: %#v", f)
		}
		// A new Server reads the same durable scope without an in-memory cache.
		owner, err := fixture.server.runControlScopes().Load(runs[lane])
		if err != nil {
			t.Fatal(err)
		}
		fresh := &Server{deps: fixture.server.deps}
		if err := fresh.validateRunControl(runs[lane], owner); err != nil {
			t.Fatal(err)
		}
		owner.Boundary = "device:another"
		if err := fresh.validateRunControl(runs[lane], owner); err == nil || err.code != "run_control_identity_mismatch" {
			t.Fatalf("device bypass: %v", err)
		}
	}
	// Same-lane selection steer is accepted for both hidden branches, even on a nonvision model.
	for _, lane := range []string{"btw", "explain"} {
		sendSelectionLaneRequest(t, conns[lane], "selection", "/api/steer", map[string]any{"runId": runs[lane], "agentKey": "mock-agent", "message": "explain", "references": []any{map[string]any{"type": "selection", "text": "selected " + lane}}})
		if f := readID(conns[lane], "selection"); !f.Data.Accepted {
			t.Fatalf("selection %s: %#v", lane, f)
		}
	}
	unblock()
	for _, lane := range lanes {
		for {
			f := readID(conns[lane], "restore")
			if f.Reason != "" {
				break
			}
		}
	}
	after, _ := fixture.chats.LoadJSONLContent(parent)
	if after != parentJSONL {
		t.Fatal("side steer changed parent JSONL")
	}
	entries, err := os.ReadDir(filepath.Join(fixture.chats.ChatDir(parent), chat.BTWRootDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two hidden branches: %v", entries)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		branch, err := fixture.chats.(*chat.FileStore).OpenBTWBranch(parent, strings.TrimSuffix(entry.Name(), ".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		history, err := branch.LoadRawMessages(20)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(history)
		for _, lane := range []string{"btw", "explain"} {
			if bytes.Count(raw, []byte("selected "+lane)) == 1 {
				found[lane] = true
			}
		}
		persisted, err := os.ReadFile(branch.Path())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(persisted, []byte(`"_type":"steer"`)) != 1 {
			t.Fatalf("hidden steer must persist once: %s", persisted)
		}
	}
	if len(found) != 2 {
		t.Fatalf("hidden replay lost selection: %v", found)
	}
	if calls.Load() != 5 {
		t.Fatalf("provider calls=%d, want three runs + two steers", calls.Load())
	}
}

func TestMissingRunScopeIsRejectedWithoutClaim(t *testing.T) {
	fixture := newTestFixture(t)
	if err := fixture.server.validateRunControl("legacy", controlscope.Scope{Transport: "ws", Lane: "main"}); err == nil || err.code != "run_control_identity_unavailable" {
		t.Fatalf("legacy accepted: %v", err)
	}
	if _, err := fixture.server.runControlScopes().Load("legacy"); err == nil {
		t.Fatal("control request claimed missing identity")
	}
}
