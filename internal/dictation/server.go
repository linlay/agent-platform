package dictation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/authn"
	"github.com/gorilla/websocket"
)

type Server struct {
	cfg         Config
	verifier    *authn.JWTVerifier
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	connections map[*websocket.Conn]struct{}
	wg          sync.WaitGroup
}

func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	if !cfg.Auth.Enabled || !cfg.Auth.RequireExpiry {
		return nil, errors.New("dictation authentication must be enabled with expiry")
	}
	v := authn.NewJWTVerifier(cfg.Auth)
	if err := v.ValidateConfiguration(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Server{cfg: cfg, verifier: v, ctx: ctx, cancel: cancel, connections: make(map[*websocket.Conn]struct{})}, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	s.cancel()
	for c := range s.connections {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, api.Failure(405, "method not allowed"))
		return
	}
	if r.URL.Path == "/healthz" {
		status := http.StatusOK
		if s.ctx.Err() != nil {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]bool{"ok": s.ctx.Err() == nil})
		return
	}
	if r.URL.Path != "/api/dictation/capabilities" && r.URL.Path != "/api/dictation/ws" {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" || !s.originAllowed(r) {
		writeJSON(w, 403, api.Failure(403, "invalid request origin or query"))
		return
	}
	token, protocol := authn.BearerSubprotocol(r)
	if h := r.Header.Get("Authorization"); h != "" {
		if !strings.HasPrefix(h, "Bearer ") {
			writeJSON(w, 401, api.Failure(401, "unauthorized"))
			return
		}
		token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	} else if token == "" {
		if cookie, err := r.Cookie("access_token"); err == nil {
			token = cookie.Value
		}
	}
	principal, err := s.verifier.Verify(token)
	if err != nil || principal.Subject == "" {
		writeJSON(w, 401, api.Failure(401, "unauthorized"))
		return
	}
	asr, err := s.cfg.newASR()
	if r.URL.Path == "/api/dictation/capabilities" {
		if asr != nil {
			asr.Close()
		}
		reason := ""
		if err != nil {
			reason = "asr_unavailable"
		}
		writeJSON(w, 200, api.Success(map[string]any{"enabled": err == nil, "reason": reason, "version": 1, "websocketPath": "/api/dictation/ws", "maxRecordingSeconds": MaxRecordingSeconds, "input": map[string]any{"codec": "pcm_s16le", "sampleRate": 16000, "channels": 1, "frameMs": 20}}))
		return
	}
	if err != nil {
		writeJSON(w, 503, api.Failure(503, "dictation ASR unavailable"))
		return
	}
	defer asr.Close()
	header := http.Header{}
	if protocol != "" {
		header.Set("Sec-WebSocket-Protocol", protocol)
	}
	c, err := (&websocket.Upgrader{CheckOrigin: s.originAllowed}).Upgrade(w, r, header)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = c.Close()
		return
	}
	s.connections[c] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { _ = c.Close(); s.mu.Lock(); delete(s.connections, c); s.mu.Unlock(); s.wg.Done() }()
	// Bounded startup + 60s capture + finalization; the credential cannot outlive exp.
	deadline := time.Now().Add(90 * time.Second)
	if expiry := time.Unix(authn.NumericDate(principal.Claims["exp"]), 0); expiry.Before(deadline) {
		deadline = expiry
	}
	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopClose()
	c.SetReadLimit(4096)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var writeMu sync.Mutex
	send := func(kind, text string) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return c.WriteJSON(map[string]any{"type": kind, "text": text})
	}
	kind, data, err := c.ReadMessage()
	if err != nil {
		return
	}
	if kind != websocket.TextMessage || control(data) != "start" {
		_ = send("error", "invalid_request")
		return
	}
	audio := make(chan []byte, 100)
	var ready atomic.Bool
	readDone := make(chan struct{})
	var recordingDeadline *time.Timer
	go func() {
		defer close(readDone)
		stopped := false
		defer func() {
			if !stopped {
				close(audio)
			}
		}()
		_ = c.SetReadDeadline(deadline)
		total := 0
		tail := false
		for {
			kind, data, err := c.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			if !ready.Load() || stopped {
				_ = send("error", "invalid_request")
				cancel()
				return
			}
			if kind == websocket.TextMessage && control(data) == "stop" {
				stopped = true
				recordingDeadline.Stop()
				close(audio)
				continue // Keep detecting disconnects during ASR finalization.
			}
			if kind != websocket.BinaryMessage || tail || len(data) == 0 || len(data) > 640 || len(data)%2 != 0 || total+len(data) > 1920000 {
				_ = send("error", "invalid_audio")
				cancel()
				return
			}
			total += len(data)
			tail = len(data) < 640
			select {
			case audio <- data:
			case <-ctx.Done():
				return
			default:
				_ = send("error", "audio_overflow")
				cancel()
				return
			}
		}
	}()
	defer func() {
		if recordingDeadline != nil {
			recordingDeadline.Stop()
		}
	}()
	text, err := asr.Recognize(ctx, audio, "manual", func(text string, _ bool) {
		if send("partial", text) != nil {
			cancel()
		}
	}, func() error {
		// 60 seconds of audio plus a bounded network delivery allowance.
		recordingDeadline = time.AfterFunc(65*time.Second, cancel)
		ready.Store(true)
		if err := send("ready", ""); err != nil {
			return err
		}
		return nil
	})
	if ctx.Err() == nil {
		if err != nil {
			_ = send("error", "recognition_failed")
		} else if strings.TrimSpace(text) == "" {
			_ = send("error", "no_speech")
		} else {
			_ = send("final", text)
		}
	}
	cancel()
	_ = c.Close()
	<-readDone
}

func control(data []byte) string {
	var m struct {
		Type string `json:"type"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return ""
	}
	return m.Type
}
