package server

import (
	"bytes"
	"maps"
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

func TestChannelImportQueryUsesServerModeInboundChannel(t *testing.T) {
	const (
		queryID = "req_server_channel_import"
		runID   = "run_server_channel_import"
		chatID  = "chat_server_channel_import"
	)

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 16
			cfg.WebSocket.PingInterval = 30000
			cfg.Channels = []config.ChannelConfig{{
				ID:        "public-entry",
				Mode:      config.ChannelModeServer,
				Transport: config.ChannelTransportWebSocket,
				Protocol:  config.ChannelProtocolPlatformWS,
				Endpoint:  config.ChannelEndpointConfig{Path: "/ws/channel"},
			}}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Server Channel Agent",
				"mode: CHANNEL",
				"channelConfig:",
				"  channelId: public-entry",
				"  remoteAgentKey: kbase-thqhcs",
			})
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	peer, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/channel?channelId=public-entry", nil)
	if err != nil {
		t.Fatalf("dial channel websocket: %v", err)
	}
	defer peer.Close()
	readConnectedPush(t, peer)

	queryDone := make(chan struct {
		status int
		body   string
	}, 1)
	go func() {
		body := bytes.NewBufferString(`{
			"requestId":"` + queryID + `",
			"runId":"` + runID + `",
			"chatId":"` + chatID + `",
			"agentKey":"mock-agent",
			"message":"hello channel",
			"stream":false,
			"includeFullText":true
		}`)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/query", body)
		if err != nil {
			queryDone <- struct {
				status int
				body   string
			}{status: 0, body: err.Error()}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			queryDone <- struct {
				status int
				body   string
			}{status: 0, body: err.Error()}
			return
		}
		defer resp.Body.Close()
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		queryDone <- struct {
			status int
			body   string
		}{status: resp.StatusCode, body: buf.String()}
	}()

	queryFrame := readChannelRequestFrame(t, peer, "/api/query", queryID)
	if queryFrame["frame"] != "request" || queryFrame["type"] != "/api/query" || queryFrame["id"] != queryID {
		t.Fatalf("unexpected channel query frame %#v", queryFrame)
	}
	payload, ok := queryFrame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected query payload object, got %#v", queryFrame["payload"])
	}
	if payload["agentKey"] != "kbase-thqhcs" {
		t.Fatalf("expected remote agent key kbase-thqhcs, got %#v", payload["agentKey"])
	}

	steerBody := bytes.NewBufferString(`{
		"runId":"` + runID + `",
		"agentKey":"mock-agent",
		"message":"focus",
		"steerId":"steer-server-channel"
	}`)
	steerReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/steer", steerBody)
	if err != nil {
		t.Fatalf("new steer request: %v", err)
	}
	steerReq.Header.Set("Content-Type", "application/json")
	steerResp, err := http.DefaultClient.Do(steerReq)
	if err != nil {
		t.Fatalf("post steer: %v", err)
	}
	defer steerResp.Body.Close()
	if steerResp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(steerResp.Body)
		t.Fatalf("expected steer 200, got %d: %s", steerResp.StatusCode, buf.String())
	}

	steerFrame := readChannelRequestFrame(t, peer, "/api/steer", "steer-server-channel")
	if steerFrame["frame"] != "request" || steerFrame["type"] != "/api/steer" || steerFrame["id"] != "steer-server-channel" {
		t.Fatalf("unexpected steer frame %#v", steerFrame)
	}

	writeUpstreamStreamFrame(t, peer, queryID, "s_"+runID, map[string]any{
		"seq":   1,
		"type":  "content.delta",
		"runId": "remote-run",
		"delta": "server channel answer",
	}, "")
	writeUpstreamStreamFrame(t, peer, queryID, "s_"+runID, map[string]any{
		"seq":   2,
		"type":  "run.complete",
		"runId": "remote-run",
	}, "")
	writeUpstreamStreamFrame(t, peer, queryID, "s_"+runID, nil, "done")

	select {
	case result := <-queryDone:
		if result.status != http.StatusOK {
			t.Fatalf("expected query 200, got %d: %s", result.status, result.body)
		}
		if !strings.Contains(result.body, "server channel answer") {
			t.Fatalf("expected query body to contain channel answer, got %s", result.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for query response")
	}
}

func TestChannelImportServerModeRequiresConnectedPeer(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.Channels = []config.ChannelConfig{{
				ID:        "public-entry",
				Mode:      config.ChannelModeServer,
				Transport: config.ChannelTransportWebSocket,
				Protocol:  config.ChannelProtocolPlatformWS,
				Endpoint:  config.ChannelEndpointConfig{Path: "/ws/channel"},
			}}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Server Channel Agent",
				"mode: CHANNEL",
				"channelConfig:",
				"  channelId: public-entry",
				"  remoteAgentKey: kbase-thqhcs",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"hello"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel public-entry is not connected") {
		t.Fatalf("expected not connected message, got %s", rec.Body.String())
	}
}

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
	if timestamp, ok := runStarted["timestamp"].(float64); !ok || timestamp < 1_000_000_000_000 {
		t.Fatalf("expected epoch-ms run.started timestamp, got %#v", runStarted)
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
	if timestamp, ok := runFinished["timestamp"].(float64); !ok || timestamp < 1_000_000_000_000 {
		t.Fatalf("expected epoch-ms run.finished timestamp, got %#v", runFinished)
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
		// Channel fixtures model a contract-compliant remote producer. Production
		// must reject missing timestamps rather than applying this default.
		if _, ok := event["timestamp"]; !ok {
			event = maps.Clone(event)
			event["timestamp"] = int64(1_700_000_000_000)
		}
		frame["event"] = event
	}
	if reason != "" {
		frame["reason"] = reason
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write upstream stream frame: %v", err)
	}
}

func readChannelRequestFrame(t *testing.T, conn *gws.Conn, frameType string, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set channel websocket read deadline: %v", err)
		}
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read channel websocket frame: %v", err)
		}
		if frame["frame"] == "request" && frame["type"] == frameType && frame["id"] == id {
			return frame
		}
	}
	t.Fatalf("timed out waiting for channel request frame type=%s id=%s", frameType, id)
	return nil
}
