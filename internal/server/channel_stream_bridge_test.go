package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestChannelImportStreamOnlySynthesizesControlPushes(t *testing.T) {
	const (
		queryID = "req_channel_stream_only"
		runID   = "run_channel_stream_only"
		chatID  = "chat_channel_stream_only"
	)

	releaseAnswer := make(chan struct{})
	release := func() {
		select {
		case <-releaseAnswer:
		default:
			close(releaseAnswer)
		}
	}
	defer release()

	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/channel" {
			t.Fatalf("expected /ws/channel, got %s", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade upstream websocket: %v", err)
		}
		defer conn.Close()
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read upstream websocket frame: %v", err)
		}
		captured <- frame

		writeUpstreamStreamFrame(t, conn, "other-request", "s_other", map[string]any{
			"seq":   1,
			"type":  "run.complete",
			"runId": "remote-other-run",
		}, "")
		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, map[string]any{
			"seq":        2,
			"type":       "awaiting.ask",
			"runId":      "remote-run",
			"awaitingId": "await-channel",
			"mode":       "approval",
			"timeout":    600,
			"approvals": []map[string]any{{
				"id":    "approval-1",
				"title": "Approve remote action",
			}},
		}, "")

		select {
		case <-releaseAnswer:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting to release upstream answer")
		}

		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, map[string]any{
			"seq":        3,
			"type":       "awaiting.answer",
			"runId":      "remote-run",
			"awaitingId": "await-channel",
			"mode":       "approval",
			"status":     "answered",
			"submitId":   "submit-channel",
		}, "")
		writeUpstreamStreamFrame(t, conn, "other-request", "s_other", map[string]any{
			"seq":   4,
			"type":  "content.delta",
			"runId": "remote-other-run",
			"delta": "wrong stream",
		}, "")
		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, map[string]any{
			"seq":           5,
			"type":          "artifact.publish",
			"runId":         "remote-run",
			"artifactCount": 1,
			"artifacts": []map[string]any{{
				"artifactId": "artifact-channel",
				"name":       "report.md",
				"mimeType":   "text/markdown",
				"sizeBytes":  123,
				"sha256":     "abc123",
				"url":        "/api/resource?file=chat_channel_stream_only%2Freport.md",
			}},
		}, "")
		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, map[string]any{
			"seq":   6,
			"type":  "content.delta",
			"runId": "remote-run",
			"delta": "channel answer",
		}, "")
		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, map[string]any{
			"seq":   7,
			"type":  "run.complete",
			"runId": "remote-run",
		}, "")
		writeUpstreamStreamFrame(t, conn, queryID, "s_"+runID, nil, "done")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 16
			cfg.WebSocket.PingInterval = 30000
			cfg.Channels = []config.ChannelConfig{{
				ID:        "peer-a",
				Mode:      config.ChannelModeClient,
				Transport: config.ChannelTransportWebSocket,
				Protocol:  config.ChannelProtocolPlatformWS,
				Endpoint: config.ChannelEndpointConfig{
					URL: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/ws/channel",
				},
			}}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Channel Agent",
				"mode: CHANNEL",
				"channelConfig:",
				"  channelId: peer-a",
				"  remoteAgentKey: coder",
			})
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query",
		ID:    queryID,
		Payload: ws.MarshalPayload(map[string]any{
			"requestId": queryID,
			"chatId":    chatID,
			"runId":     runID,
			"agentKey":  "mock-agent",
			"message":   "hello channel",
		}),
	}); err != nil {
		t.Fatalf("write channel query: %v", err)
	}

	select {
	case frame := <-captured:
		if frame["frame"] != "request" || frame["type"] != "/api/query" || frame["id"] != queryID {
			t.Fatalf("unexpected upstream query frame %#v", frame)
		}
		payload, _ := frame["payload"].(map[string]any)
		if payload["agentKey"] != "coder" {
			t.Fatalf("expected remote agent key coder, got %#v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for upstream query")
	}

	chatCreated := pushFrameDataMap(t, waitForPushFrameType(t, conn, "chat.created"))
	if chatCreated["chatId"] != chatID || chatCreated["agentKey"] != "mock-agent" {
		t.Fatalf("unexpected chat.created push %#v", chatCreated)
	}
	runStarted := pushFrameDataMap(t, waitForPushFrameType(t, conn, "run.started"))
	if runStarted["runId"] != runID || runStarted["chatId"] != chatID || runStarted["agentKey"] != "mock-agent" {
		t.Fatalf("unexpected run.started push %#v", runStarted)
	}
	awaitingAsk := pushFrameDataMap(t, waitForPushFrameType(t, conn, "awaiting.asking"))
	if awaitingAsk["chatId"] != chatID || awaitingAsk["runId"] != runID || awaitingAsk["agentKey"] != "mock-agent" ||
		awaitingAsk["awaitingId"] != "await-channel" || awaitingAsk["mode"] != "approval" {
		t.Fatalf("unexpected awaiting.asking push %#v", awaitingAsk)
	}

	summaries := loadChatSummariesForTest(t, fixture.server)
	if len(summaries) != 1 || summaries[0].Awaiting == nil ||
		summaries[0].Awaiting.AwaitingID != "await-channel" || summaries[0].Awaiting.RunID != runID {
		t.Fatalf("expected pending awaiting summary, got %#v", summaries)
	}

	release()

	awaitingAnswer := pushFrameDataMap(t, waitForPushFrameType(t, conn, "awaiting.answered"))
	if awaitingAnswer["chatId"] != chatID || awaitingAnswer["runId"] != runID ||
		awaitingAnswer["awaitingId"] != "await-channel" || awaitingAnswer["status"] != "answered" {
		t.Fatalf("unexpected awaiting.answered push %#v", awaitingAnswer)
	}
	summaries = loadChatSummariesForTest(t, fixture.server)
	if len(summaries) != 1 || summaries[0].Awaiting != nil {
		t.Fatalf("expected pending awaiting to be cleared, got %#v", summaries)
	}

	resourcePushed := pushFrameDataMap(t, waitForPushFrameType(t, conn, "resource.pushed"))
	if resourcePushed["chatId"] != chatID || resourcePushed["artifactId"] != "artifact-channel" ||
		resourcePushed["name"] != "report.md" || resourcePushed["mimeType"] != "text/markdown" ||
		resourcePushed["sha256"] != "abc123" {
		t.Fatalf("unexpected resource.pushed push %#v", resourcePushed)
	}
	if sizeBytes, ok := resourcePushed["sizeBytes"].(float64); !ok || int(sizeBytes) != 123 {
		t.Fatalf("unexpected resource.pushed size %#v", resourcePushed)
	}

	runFinished := pushFrameDataMap(t, waitForPushFrameType(t, conn, "run.finished"))
	if runFinished["runId"] != runID || runFinished["chatId"] != chatID {
		t.Fatalf("unexpected run.finished push %#v", runFinished)
	}
	chatUpdated := pushFrameDataMap(t, waitForPushFrameType(t, conn, "chat.updated"))
	if chatUpdated["chatId"] != chatID || chatUpdated["lastRunId"] != runID || chatUpdated["lastRunContent"] != "channel answer" {
		t.Fatalf("unexpected chat.updated push %#v", chatUpdated)
	}
}

func writeUpstreamStreamFrame(t *testing.T, conn *gws.Conn, id string, streamID string, event map[string]any, reason string) {
	t.Helper()
	frame := map[string]any{
		"frame":    "stream",
		"id":       id,
		"streamId": streamID,
	}
	if event != nil {
		frame["event"] = event
	}
	if reason != "" {
		frame["reason"] = reason
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write upstream stream frame: %v", err)
	}
}
