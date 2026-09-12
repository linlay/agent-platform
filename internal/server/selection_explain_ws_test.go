package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	platformws "agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type selectionLaneFrame struct {
	Frame  string `json:"frame"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Code   int    `json:"code"`
	Msg    string `json:"msg"`
	Data   struct {
		Accepted bool `json:"accepted"`
	} `json:"data"`
	Event *stream.EventData `json:"event"`
}

func readSelectionLaneFrame(t *testing.T, conn *gws.Conn) selectionLaneFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var frame selectionLaneFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read selection lane: %v", err)
	}
	return frame
}

func sendSelectionLaneRequest(t *testing.T, conn *gws.Conn, requestID, route string, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(platformws.RequestFrame{
		Frame: platformws.FrameRequest, ID: requestID, Type: route, Payload: marshalPayload(payload),
	}); err != nil {
		t.Fatalf("send %s: %v", route, err)
	}
}

func TestSelectionExplainDetachAttachAndInterruptKeepExistingRunSemantics(t *testing.T) {
	var blockProvider atomic.Bool
	providerStarted := make(chan struct{}, 1)
	releaseProvider := make(chan struct{})
	hub := platformws.NewHub()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if !blockProvider.Load() {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"parent"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"explanation in progress"}}]}`)
		select {
		case providerStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-releaseProvider:
		}
	}, testFixtureOptions{notifications: hub})
	defer close(releaseProvider)
	const chatID = "chat-selection-control"
	serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent"}`)
	blockProvider.Store(true)
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+
		"/ws?source=desktop-selection-explain&surfaceId=desktop-selection-explain&deviceId=device-selection", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	sendSelectionLaneRequest(t, conn, "explain", "/api/btw", map[string]any{"chatId": chatID, "message": "explain selection"})
	var runID string
	for runID == "" {
		frame := readSelectionLaneFrame(t, conn)
		if frame.Frame == platformws.FrameError {
			t.Fatalf("explanation failed: %s %s", frame.Type, frame.Msg)
		}
		if frame.Event != nil && frame.Event.Type == "run.start" {
			runID, _ = frame.Event.Value("runId").(string)
		}
	}
	select {
	case <-providerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("explanation did not reach the test provider")
	}
	targetBefore := resolveRunWebClientTarget(fixture.runs, runID)
	sendSelectionLaneRequest(t, conn, "detach", "/api/detach", map[string]any{"runId": runID, "agentKey": "mock-agent"})
	for {
		frame := readSelectionLaneFrame(t, conn)
		if frame.ID != "detach" {
			continue
		}
		if frame.Frame != platformws.FrameResponse || frame.Code != 0 || !frame.Data.Accepted {
			t.Fatalf("detach failed: %#v", frame)
		}
		break
	}
	if status, ok := fixture.runs.RunStatus(runID); !ok || status.CompletedAt != 0 || status.State == contracts.RunLoopStateCancelled {
		t.Fatalf("detaching the explanation must leave its Run active: %#v", status)
	}
	sendSelectionLaneRequest(t, conn, "attach", "/api/attach", map[string]any{"runId": runID, "agentKey": "mock-agent", "lastSeq": 0})
	for {
		frame := readSelectionLaneFrame(t, conn)
		if frame.ID != "attach" {
			continue
		}
		if frame.Frame != platformws.FrameStream {
			t.Fatalf("attach failed: %#v", frame)
		}
		if frame.Event != nil && frame.Event.Type == "run.start" {
			break
		}
	}
	if targetAfter := resolveRunWebClientTarget(fixture.runs, runID); targetAfter != targetBefore {
		t.Fatal("explanation attach overwrote the Run's existing WebClient action target")
	}
	sendSelectionLaneRequest(t, conn, "interrupt", "/api/interrupt", map[string]any{"runId": runID, "agentKey": "mock-agent"})
	accepted, terminal := false, false
	for !accepted || !terminal {
		frame := readSelectionLaneFrame(t, conn)
		if frame.Frame == platformws.FrameError {
			t.Fatalf("interrupt failed: %s %s", frame.Type, frame.Msg)
		}
		if frame.ID == "interrupt" && frame.Frame == platformws.FrameResponse {
			if frame.Code != 0 || !frame.Data.Accepted {
				t.Fatalf("interrupt was not accepted: %#v", frame)
			}
			accepted = true
		}
		if frame.ID == "attach" && frame.Reason != "" {
			terminal = true
		}
	}
	if status, ok := fixture.runs.RunStatus(runID); !ok || status.State != contracts.RunLoopStateCancelled {
		t.Fatalf("explicit Stop did not cancel the explanation Run: %#v", status)
	}
}

type selectionLaneUntrustedAuthenticator struct{}

func (selectionLaneUntrustedAuthenticator) VerifyToken(ctx context.Context, _ string) (platformws.AuthSession, error) {
	return platformws.AuthSession{Context: ctx, Subject: "web-user", DeviceID: "device-selection", DeviceIDVerified: true, Scope: "openid"}, nil
}

func TestSelectionExplainWebSocketSourceCannotGrantAppPrivileges(t *testing.T) {
	fixture := newTestFixture(t)
	handler := platformws.NewHandler(fixture.cfg.WebSocket, platformws.NewHub(), selectionLaneUntrustedAuthenticator{})
	handler.RegisterRoute("/api/btw", fixture.server.wsBTW)
	server := newLoopbackServer(t, handler)
	defer server.Close()
	conn, response, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+
		"/ws?source=desktop-selection-explain&surfaceId=desktop-selection-explain&deviceId=device-selection", nil)
	if conn != nil {
		_ = conn.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("spoofed source must fail before websocket registration: response=%v err=%v", response, err)
	}
}
