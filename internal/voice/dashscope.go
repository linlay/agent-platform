// Package voice owns realtime speech media and the voice session protocol.
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/httpclient"
	"agent-platform/internal/models"
	"github.com/gorilla/websocket"
)

const (
	startupTimeout = 5 * time.Second
	// A silently dropped reused socket must fail fast enough for the fresh
	// retry to fit inside the 2s startup audio buffer.
	reusedStartupTimeout = time.Second
	finishTimeout        = 10 * time.Second
	writeTimeout         = 5 * time.Second
	maxStartupPCM        = 64000
	maxRecordingPCM      = 1920000
	maxTranscriptChars   = 4000
)

// errProviderTransport marks socket write/read failures, as opposed to events
// the provider sent on a live socket.
var errProviderTransport = errors.New("voice provider connection")

// One socket per ASR/TTS client, reused only after task-finished. A cancelled or
// failed task closes the socket: untagged late binary frames cannot cross tasks.
type dashScopeSocket struct {
	taskMu   sync.Mutex
	mu       sync.Mutex
	conn     *websocket.Conn
	model    models.ModelDefinition
	provider models.ProviderDefinition
	endpoint string
}

type dashScopeEvent struct {
	Header struct {
		Event  string `json:"event"`
		TaskID string `json:"task_id"`
	} `json:"header"`
	Payload struct {
		Output struct {
			Sentence struct {
				Begin     int    `json:"begin_time"`
				Text      string `json:"text"`
				Final     bool   `json:"sentence_end"`
				Heartbeat bool   `json:"heartbeat"`
			} `json:"sentence"`
		} `json:"output"`
	} `json:"payload"`
}

func newDashScopeSocket(model models.ModelDefinition, provider models.ProviderDefinition, typ, protocol string) (*dashScopeSocket, error) {
	if model.Type != typ || model.Protocol != protocol || strings.TrimSpace(model.ModelID) == "" {
		return nil, fmt.Errorf("voice model requires type %s and protocol %s", typ, protocol)
	}
	if !provider.HasCredential() {
		return nil, errors.New("voice provider credential is not configured")
	}
	u, err := url.Parse(provider.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid voice provider base URL")
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return nil, errors.New("voice provider requires an HTTP(S) or WS(S) URL")
	}
	path, err := url.Parse(provider.ResolvedEndpointPath(protocol))
	if err != nil || path.IsAbs() || path.Host != "" || path.Fragment != "" {
		return nil, errors.New("invalid voice endpoint path")
	}
	u = u.ResolveReference(path)
	return &dashScopeSocket{model: model, provider: provider, endpoint: u.String()}, nil
}

func (s *dashScopeSocket) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// begin serializes tasks, refreshes credentials on reconnect, and binds only the
// media task to ctx. It never owns or cancels an Agent Run. reused reports that
// the socket was kept from an earlier finished task.
func (s *dashScopeSocket) begin(ctx context.Context) (*websocket.Conn, func(bool), bool, error) {
	s.taskMu.Lock()
	if err := ctx.Err(); err != nil {
		s.taskMu.Unlock()
		return nil, nil, false, err
	}
	s.mu.Lock()
	c := s.conn
	reused := c != nil
	if c == nil {
		headers := http.Header{}
		for k, v := range s.provider.Protocol(s.model.Protocol).Headers {
			headers.Set(k, v)
		}
		for k, v := range s.model.Headers {
			headers.Set(k, v)
		}
		name, value, err := s.provider.AuthHeader("")
		if err != nil {
			s.mu.Unlock()
			s.taskMu.Unlock()
			return nil, nil, false, errors.New("voice provider credential unavailable")
		}
		headers.Set(name, value)
		var response *http.Response
		c, response, err = httpclient.DialWebSocket(ctx, s.endpoint, headers, startupTimeout)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			s.mu.Unlock()
			s.taskMu.Unlock()
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			return nil, nil, false, errors.New("voice provider websocket connection failed")
		}
		c.SetReadLimit(1 << 20)
		s.conn = c
	}
	s.mu.Unlock()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.Close(); close(done) })
	return c, func(success bool) {
		if !stop() {
			<-done
		}
		if !success || ctx.Err() != nil {
			s.close()
		}
		s.taskMu.Unlock()
	}, reused, nil
}

// start sends run-task and waits for task-started. Nothing of this task has
// reached the provider before task-started, so a reused socket that the
// provider or network closed while idle is replaced once by a fresh one.
func (s *dashScopeSocket) start(ctx context.Context, id string, payload map[string]any) (*websocket.Conn, func(bool), error) {
	for retry := true; ; retry = false {
		conn, end, reused, err := s.begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		timeout := startupTimeout
		if reused && retry {
			timeout = reusedStartupTimeout
		}
		if err = dashCommand(conn, id, "run-task", payload); err == nil {
			err = dashStarted(conn, id, timeout)
		}
		if err == nil {
			return conn, end, nil
		}
		end(false)
		if !retry || !reused || ctx.Err() != nil || !errors.Is(err, errProviderTransport) {
			return nil, nil, err
		}
	}
}

func dashCommand(c *websocket.Conn, id, action string, payload map[string]any) error {
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.WriteJSON(map[string]any{"header": map[string]any{"action": action, "task_id": id, "streaming": "duplex"}, "payload": payload}); err != nil {
		return fmt.Errorf("%w: write failed", errProviderTransport)
	}
	return nil
}

func dashRead(c *websocket.Conn, id string, deadline time.Time) ([]byte, *dashScopeEvent, error) {
	_ = c.SetReadDeadline(deadline)
	for {
		kind, data, err := c.ReadMessage()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: read failed or timed out", errProviderTransport)
		}
		if kind == websocket.BinaryMessage {
			return data, nil, nil
		}
		if kind != websocket.TextMessage {
			continue
		}
		var event dashScopeEvent
		if json.Unmarshal(data, &event) != nil {
			return nil, nil, errors.New("invalid voice provider event")
		}
		if event.Header.TaskID != id {
			continue
		}
		if event.Header.Event == "task-failed" {
			return nil, nil, errors.New("voice provider task failed")
		}
		return nil, &event, nil
	}
}

func dashStarted(c *websocket.Conn, id string, timeout time.Duration) error {
	_, event, err := dashRead(c, id, time.Now().Add(timeout))
	if err != nil {
		return err
	}
	if event == nil || event.Header.Event != "task-started" {
		return errors.New("expected voice task-started")
	}
	return nil
}
