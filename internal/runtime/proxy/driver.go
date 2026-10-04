package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/httpclient"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"

	gws "github.com/gorilla/websocket"
)

// EventSink handles normalized events and recording; the wire driver does not
// own usage, persistence, RunManager or completion notifications.
type EventSink interface {
	Publish(*int64, stream.EventData) (stream.EventData, error)
	Error(error)
	ErrorAfter(error, int64)
	ObserverCount() int
}

// ChannelConnection is one existing inbound connection. Its owner keeps the
// socket and multiplexing lifetime; a Proxy run only owns the opened request.
type ChannelConnection interface {
	OpenRequest(map[string]any) (<-chan []byte, func(), error)
	SendFrame(any) bool
}
type ChannelConnections interface {
	Connection(string) (ChannelConnection, bool)
}

type Driver struct {
	Chats    chat.Store
	Tickets  TicketIssuer
	Channels ChannelConnections
}

func (d *Driver) Stream(runCtx context.Context, prepared runtimetypes.PreparedQuery, route *Route, events EventSink, startup chan<- error) {
	if UpstreamTransport(prepared.AgentDef.ProxyConfig) == "sse" {
		d.runSSE(runCtx, prepared, events, startup)
		return
	}
	if startup != nil {
		startup <- nil
	}

	if strings.TrimSpace(prepared.AgentDef.ProxyConfig.ChannelID) != "" {
		d.runInboundChannel(runCtx, prepared, route, events)
		return
	}

	upstreamURL, header, err := WebSocketTarget(prepared.AgentDef.ProxyConfig)
	if err != nil {
		events.Error(err)
		return
	}

	upstream, _, err := gws.DefaultDialer.DialContext(runCtx, upstreamURL, header)
	if err != nil {
		events.Error(fmt.Errorf("proxy websocket dial failed: %w", err))
		return
	}
	defer upstream.Close()
	// DialContext only covers the handshake. Close an established connection
	// on cancellation so a silent upstream cannot keep ReadMessage blocked.
	stopCancel := context.AfterFunc(runCtx, func() { _ = upstream.Close() })
	defer stopCancel()

	proxyReferences, err := PrepareReferences(d.Chats, d.Tickets, ReferenceOptions{
		ChatID:          prepared.Req.ChatID,
		RunID:           prepared.Req.RunID,
		Subject:         prepared.Session.Subject,
		ResourceBaseURL: prepared.ResourceBaseURL,
		WorkspaceRoot:   prepared.Session.WorkspaceRoot,
		References:      prepared.Req.References,
	})
	if err != nil {
		events.Error(err)
		return
	}
	if err := upstream.WriteJSON(QueryPayloadWithWorkspace(prepared.Req, prepared.AgentDef.ProxyConfig, proxyReferences, prepared.Session.WorkspaceRoot)); err != nil {
		events.Error(fmt.Errorf("proxy websocket write failed: %w", err))
		return
	}

	// Send the initial query before starting the sole control-message writer.
	writeDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				writeDone <- runCtx.Err()
				return
			case <-route.Done:
				writeDone <- nil
				return
			case msg := <-route.SendQueue:
				if err := upstream.WriteJSON(msg); err != nil {
					writeDone <- err
					return
				}
			}
		}
	}()

	var seq int64
	terminalSeen := false
	for {
		select {
		case err := <-writeDone:
			if err != nil && !terminalSeen {
				events.Error(fmt.Errorf("proxy websocket write loop failed: %w", err))
			}
			return
		default:
		}

		_, data, err := upstream.ReadMessage()
		if err != nil {
			if !terminalSeen {
				events.Error(fmt.Errorf("proxy websocket read failed: %w", err))
			}
			return
		}
		frame, ok, decodeErr := DecodeFrameAt(data, "proxy.websocket.event")
		if decodeErr != nil {
			terminalSeen = true
			events.Error(decodeErr)
			return
		}
		if !ok || !FrameMatchesRequest(frame, prepared.Req.RequestID) {
			continue
		}
		if err := FrameError(frame); err != nil {
			terminalSeen = true
			events.Error(err)
			return
		}
		if !frame.HasEvent {
			if strings.EqualFold(frame.Frame, "stream") && frame.Reason != "" {
				terminalSeen = true
				return
			}
			continue
		}
		event, publishErr := events.Publish(&seq, frame.Event)
		if publishErr != nil {
			terminalSeen = true
			events.Error(publishErr)
			return
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
}

func (d *Driver) runInboundChannel(
	runCtx context.Context,
	prepared runtimetypes.PreparedQuery,
	route *Route,
	events EventSink,
) {
	proxy := prepared.AgentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.ChannelID) == "" {
		events.Error(fmt.Errorf("CHANNEL agent missing channelConfig.channelId"))
		return
	}
	if d.Channels == nil {
		events.Error(fmt.Errorf("channel %s connection provider is not configured", proxy.ChannelID))
		return
	}
	upstream, ok := d.Channels.Connection(proxy.ChannelID)
	if !ok || upstream == nil {
		events.Error(fmt.Errorf("channel %s is not connected", proxy.ChannelID))
		return
	}

	proxyReferences, err := PrepareReferences(d.Chats, d.Tickets, ReferenceOptions{
		ChatID:          prepared.Req.ChatID,
		RunID:           prepared.Req.RunID,
		Subject:         prepared.Session.Subject,
		ResourceBaseURL: prepared.ResourceBaseURL,
		WorkspaceRoot:   prepared.Session.WorkspaceRoot,
		References:      prepared.Req.References,
	})
	if err != nil {
		events.Error(err)
		return
	}

	initial := QueryPayloadWithWorkspace(prepared.Req, proxy, proxyReferences, prepared.Session.WorkspaceRoot)
	frames, cleanup, err := upstream.OpenRequest(initial)
	if err != nil {
		events.Error(fmt.Errorf("channel %s request failed: %w", proxy.ChannelID, err))
		return
	}
	defer cleanup()

	writeDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				writeDone <- runCtx.Err()
				return
			case <-route.Done:
				writeDone <- nil
				return
			case msg := <-route.SendQueue:
				if !upstream.SendFrame(msg) {
					writeDone <- fmt.Errorf("channel %s write failed", proxy.ChannelID)
					return
				}
			}
		}
	}()

	var seq int64
	terminalSeen := false
	for {
		select {
		case err := <-writeDone:
			if err != nil && !terminalSeen {
				events.Error(fmt.Errorf("channel websocket write loop failed: %w", err))
			}
			return
		case <-runCtx.Done():
			if !terminalSeen {
				events.Error(runCtx.Err())
			}
			return
		case data, ok := <-frames:
			if !ok {
				if !terminalSeen {
					events.Error(fmt.Errorf("channel %s disconnected", proxy.ChannelID))
				}
				return
			}
			frame, ok, decodeErr := DecodeFrameAt(data, "proxy.channel.event")
			if decodeErr != nil {
				terminalSeen = true
				events.Error(decodeErr)
				return
			}
			if !ok || !FrameMatchesRequest(frame, prepared.Req.RequestID) {
				continue
			}
			if err := FrameError(frame); err != nil {
				terminalSeen = true
				events.Error(err)
				return
			}
			if !frame.HasEvent {
				if strings.EqualFold(frame.Frame, "stream") && frame.Reason != "" {
					terminalSeen = true
					return
				}
				continue
			}
			event, publishErr := events.Publish(&seq, frame.Event)
			if publishErr != nil {
				terminalSeen = true
				events.Error(publishErr)
				return
			}
			switch event.Type {
			case "run.complete", "run.error", "run.cancel":
				terminalSeen = true
				return
			}
		}
	}
}

func (d *Driver) runSSE(
	runCtx context.Context,
	prepared runtimetypes.PreparedQuery,
	events EventSink,
	startup chan<- error,
) {
	startupResolved := false
	startupObserverReady := false
	resolveStartup := func(err error) {
		if startup == nil || startupResolved {
			return
		}
		startupResolved = true
		startup <- err
	}
	defer resolveStartup(nil)
	proxy := prepared.AgentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		err := fmt.Errorf("PROXY agent missing proxyConfig.baseUrl")
		resolveStartup(err)
		events.Error(err)
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"
	proxyReferences, err := PrepareReferences(d.Chats, d.Tickets, ReferenceOptions{
		ChatID:          prepared.Req.ChatID,
		RunID:           prepared.Req.RunID,
		Subject:         prepared.Session.Subject,
		ResourceBaseURL: prepared.ResourceBaseURL,
		WorkspaceRoot:   prepared.Session.WorkspaceRoot,
		References:      prepared.Req.References,
	})
	if err != nil {
		resolveStartup(err)
		events.Error(err)
		return
	}
	bodyPayload := map[string]any{
		"requestId":   prepared.Req.RequestID,
		"runId":       prepared.Req.RunID,
		"chatId":      prepared.Req.ChatID,
		"agentKey":    AgentKey(proxy, prepared.Req.AgentKey),
		"role":        prepared.Req.Role,
		"message":     prepared.Req.Message,
		"accessLevel": prepared.Req.AccessLevel,
		"references":  proxyReferences,
		"params":      ForwardParams(prepared.Req, proxy, prepared.Session.WorkspaceRoot),
		"model":       prepared.Req.Model,
		"scene":       prepared.Req.Scene,
		"stream":      true,
	}
	if prepared.Req.Hidden != nil {
		bodyPayload["hidden"] = *prepared.Req.Hidden
	}
	if prepared.Req.PlanningMode != nil {
		bodyPayload["planningMode"] = *prepared.Req.PlanningMode
	}
	if len(prepared.Req.MustUseSkills) > 0 {
		bodyPayload["mustUseSkills"] = append([]string(nil), prepared.Req.MustUseSkills...)
	}
	body, err := json.Marshal(bodyPayload)
	if err != nil {
		resolveStartup(err)
		events.Error(err)
		return
	}

	client := httpclient.NewClient(RequestTimeout(proxy))
	proxyReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		err = fmt.Errorf("failed to create proxy sse request: %w", err)
		resolveStartup(err)
		events.Error(err)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	if proxy.Token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+proxy.Token)
	}

	log.Printf("[proxy][ws] bridging websocket client to upstream sse %s (agent=%s, chatId=%s)", targetURL, prepared.AgentDef.Key, prepared.Req.ChatID)
	resp, err := client.Do(proxyReq)
	if err != nil {
		err = fmt.Errorf("proxy sse request failed: %w", err)
		resolveStartup(err)
		events.Error(err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("proxy sse upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		resolveStartup(err)
		events.Error(err)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	var seq int64
	terminalSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == stream.DoneSentinel {
			continue
		}
		event, ok, decodeErr := DecodeEventAt([]byte(payload), "proxy.sse.event")
		if decodeErr != nil {
			terminalSeen = true
			resolveStartup(decodeErr)
			events.ErrorAfter(decodeErr, seq)
			return
		}
		if !ok {
			continue
		}
		resolveStartup(nil)
		if startup != nil && !startupObserverReady {
			startupObserverReady = true
			waitForProxyStartupObserver(runCtx, events)
		}
		event, err = events.Publish(&seq, event)
		if err != nil {
			terminalSeen = true
			resolveStartup(err)
			events.ErrorAfter(err, seq)
			return
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
	if err := scanner.Err(); err != nil && !terminalSeen {
		err = fmt.Errorf("proxy sse read failed: %w", err)
		resolveStartup(err)
		events.ErrorAfter(err, seq)
	}
}

// StartQuery must validate the first upstream SSE event before the HTTP
// adapter commits a 200 response. Once validation succeeds, hold that event
// briefly until the adapter has attached its observer; otherwise an upstream
// sequence beginning above 1 could be mistaken for an expired replay window.
func waitForProxyStartupObserver(ctx context.Context, events EventSink) {
	if events == nil || events.ObserverCount() > 0 {
		return
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for events.ObserverCount() == 0 {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
		}
	}
}
