// Package voice owns realtime speech media and the voice session protocol.
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/httpclient"
	"agent-platform/internal/models"
	"agent-platform/internal/observability"
	"github.com/gorilla/websocket"
)

const (
	startupTimeout = 5 * time.Second
	// A silently dropped reused socket fails fast so the fresh retry adds
	// little delay to the first words.
	reusedStartupTimeout = time.Second
	finishTimeout        = 10 * time.Second
	writeTimeout         = 5 * time.Second
	// Listening PCM queues for the whole startup path (stale reused socket,
	// fresh dial, task-started), so a slow route starts late or fails with its
	// own error instead of overflowing first. A proxied dial took over 2s.
	maxStartupPCM      = int((reusedStartupTimeout+2*startupTimeout)/(20*time.Millisecond)) * 640
	maxRecordingPCM    = 1920000
	maxTranscriptChars = 4000
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
		Event        string `json:"event"`
		TaskID       string `json:"task_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
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
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			s.logConnectFailure(err, status, value)
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

func (s *dashScopeSocket) logConnectFailure(err error, status int, credential string) {
	s.logFailure(map[string]any{"stage": "ws_connect", "status": status}, err.Error(), credential, strings.TrimPrefix(credential, "Bearer "))
}

// logFailure records provider-supplied text for diagnosis only; public errors
// stay fixed.
func (s *dashScopeSocket) logFailure(fields map[string]any, reason string, secrets ...string) {
	// Remove exact credentials before generic redaction, and never log the endpoint.
	for _, secret := range append(secrets, s.provider.APIKey, s.endpoint) {
		if secret != "" {
			reason = strings.ReplaceAll(reason, secret, observability.HiddenToken)
		}
	}
	runes := []rune(observability.SanitizeLog(reason))
	if len(runes) > 1024 {
		runes = append(runes[:1024], '…')
	}
	fields["modelKey"], fields["protocol"], fields["reason"] = s.model.Key, s.model.Protocol, string(runes)
	observability.Log("voice", fields)
}

// taskTrace reports how far provider startup got to the session that owns the
// task: the media goroutine stores unix nanos, the event loop reads them.
type taskTrace struct{ connected, started atomic.Int64 }

type taskTraceKey struct{}

func withTaskTrace(ctx context.Context, trace *taskTrace) context.Context {
	return context.WithValue(ctx, taskTraceKey{}, trace)
}

func (t *taskTrace) phase() string {
	switch {
	case t.started.Load() != 0:
		return "streaming"
	case t.connected.Load() != 0:
		return "awaiting_task_started"
	default:
		return "connecting"
	}
}

// start sends run-task and waits for task-started. Nothing of this task has
// reached the provider before task-started, so a reused socket that the
// provider or network closed while idle is replaced once by a fresh one.
func (s *dashScopeSocket) start(ctx context.Context, id string, payload map[string]any) (*websocket.Conn, func(bool), error) {
	trace, _ := ctx.Value(taskTraceKey{}).(*taskTrace)
	if trace == nil {
		trace = &taskTrace{}
	}
	for retry := true; ; retry = false {
		trace.connected.Store(0)
		conn, end, reused, err := s.begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		trace.connected.Store(time.Now().UnixNano())
		timeout := startupTimeout
		if reused && retry {
			timeout = reusedStartupTimeout
		}
		if err = dashCommand(conn, id, "run-task", payload); err == nil {
			err = s.started(conn, id, timeout)
		}
		if err == nil {
			trace.started.Store(time.Now().UnixNano())
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

func (s *dashScopeSocket) read(c *websocket.Conn, id string, deadline time.Time) ([]byte, *dashScopeEvent, error) {
	_ = c.SetReadDeadline(deadline)
	for {
		kind, data, err := c.ReadMessage()
		if err != nil {
			var timeout net.Error
			var closed *websocket.CloseError
			switch {
			case errors.As(err, &timeout) && timeout.Timeout():
				return nil, nil, fmt.Errorf("%w: read timed out", errProviderTransport)
			case errors.As(err, &closed):
				return nil, nil, fmt.Errorf("%w: closed by provider (%d)", errProviderTransport, closed.Code)
			}
			return nil, nil, fmt.Errorf("%w: read failed", errProviderTransport)
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
			s.logFailure(map[string]any{"stage": "task_failed", "code": event.Header.ErrorCode}, event.Header.ErrorMessage)
			return nil, nil, errors.New("voice provider task failed")
		}
		return nil, &event, nil
	}
}

func (s *dashScopeSocket) started(c *websocket.Conn, id string, timeout time.Duration) error {
	_, event, err := s.read(c, id, time.Now().Add(timeout))
	if err != nil {
		return err
	}
	if event == nil || event.Header.Event != "task-started" {
		return errors.New("expected voice task-started")
	}
	return nil
}
