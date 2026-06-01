package server

import (
	"bufio"
	"bytes"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/ws"
)

func TestStatusRecorderExposesFlusherWhenUnderlyingWriterSupportsIt(t *testing.T) {
	base := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	flusher, ok := any(rec).(http.Flusher)
	if !ok {
		t.Fatalf("expected statusRecorder to implement http.Flusher")
	}

	flusher.Flush()
	if !base.Flushed {
		t.Fatalf("expected Flush to be forwarded to underlying response writer")
	}
}

func TestStatusRecorderExposesHijackerWhenUnderlyingWriterSupportsIt(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	base := &hijackableResponseWriter{
		header: make(http.Header),
		conn:   serverConn,
		rw:     bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)),
	}
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	hijacker, ok := any(rec).(http.Hijacker)
	if !ok {
		t.Fatalf("expected statusRecorder to implement http.Hijacker")
	}

	gotConn, gotRW, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	if gotConn != base.conn {
		t.Fatalf("expected hijacked conn to match underlying conn")
	}
	if gotRW != base.rw {
		t.Fatalf("expected hijacked read writer to match underlying read writer")
	}
}

func TestStatusRecorderHijackReturnsErrorWhenUnderlyingWriterDoesNotSupportIt(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}

	_, _, err := rec.Hijack()
	if err == nil {
		t.Fatalf("expected hijack to fail when underlying writer does not implement http.Hijacker")
	}
	if !strings.Contains(err.Error(), "underlying ResponseWriter does not implement http.Hijacker") {
		t.Fatalf("unexpected hijack error: %v", err)
	}
}

func TestServeHTTPLogsArrivalBeforeCompletion(t *testing.T) {
	fixture := newTestFixture(t)

	var buffer bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(originalWriter)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	logText := buffer.String()
	arrived := strings.Index(logText, "GET /api/agents (arrived)")
	completed := strings.Index(logText, "GET /api/agents -> 200")
	if arrived < 0 {
		t.Fatalf("expected arrival log, got %q", logText)
	}
	if completed < 0 {
		t.Fatalf("expected completion log, got %q", logText)
	}
	if arrived > completed {
		t.Fatalf("expected arrival log before completion log, got %q", logText)
	}
}

func TestServeHTTPLogsHideTokenQueryValues(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
	})

	var buffer bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(originalWriter)

	token := "real.jwt.value"
	req := httptest.NewRequest(http.MethodGet, "/ws?token="+token+"&mode=test", nil)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	logText := buffer.String()
	if strings.Contains(logText, token) {
		t.Fatalf("expected token to be hidden, got %q", logText)
	}
	if !strings.Contains(logText, "GET /ws?token=<HIDDEN_TOKEN>&mode=test (arrived)") {
		t.Fatalf("expected hidden token in arrival log, got %q", logText)
	}
	if !strings.Contains(logText, "GET /ws?token=<HIDDEN_TOKEN>&mode=test ->") {
		t.Fatalf("expected hidden token in completion log, got %q", logText)
	}

	monitorReq := httptest.NewRequest(http.MethodGet, "/monitor?access_token="+token+"&mode=test", nil)
	monitorRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(monitorRec, monitorReq)

	logText = buffer.String()
	if strings.Contains(logText, token) {
		t.Fatalf("expected access_token to be hidden, got %q", logText)
	}
	if !strings.Contains(logText, "GET /monitor?access_token=<HIDDEN_TOKEN>&mode=test (arrived)") {
		t.Fatalf("expected hidden access_token in arrival log, got %q", logText)
	}
	if !strings.Contains(logText, "GET /monitor?access_token=<HIDDEN_TOKEN>&mode=test ->") {
		t.Fatalf("expected hidden access_token in completion log, got %q", logText)
	}
}

func TestMonitorStaticRoutes(t *testing.T) {
	fixture := newTestFixture(t)

	tests := []struct {
		method        string
		path          string
		wantStatus    int
		wantType      string
		wantBodyParts []string
		wantAbsent    []string
	}{
		{
			method:     http.MethodGet,
			path:       "/monitor",
			wantStatus: http.StatusOK,
			wantType:   "text/html",
			wantBodyParts: []string{
				"智能体平台监控",
				`name="referrer" content="no-referrer"`,
				"token-input",
				"apply-token",
				"clear-token",
				`name="connection-view" value="active"`,
				`name="connection-view" value="all"`,
				"/monitor/monitor.css",
				"/monitor/monitor.js",
				"frame / id",
				"event / topic",
			},
		},
		{
			method:        http.MethodGet,
			path:          "/monitor/monitor.css",
			wantStatus:    http.StatusOK,
			wantType:      "text/css",
			wantBodyParts: []string{".metric-grid", ".connections-table", ".copy-user-agent"},
		},
		{
			method:        http.MethodGet,
			path:          "/monitor/monitor.js",
			wantStatus:    http.StatusOK,
			wantType:      "text/javascript",
			wantBodyParts: []string{"requestJSON", "Authorization", "access_token", "history.replaceState", "sessionId", "connectionView", "active", "copyPayloadText", "copyUserAgentText"},
		},
		{
			method:     http.MethodGet,
			path:       "/monitor/not-found.js",
			wantStatus: http.StatusNotFound,
		},
		{
			method:     http.MethodPost,
			path:       "/monitor",
			wantStatus: http.StatusMethodNotAllowed,
			wantType:   "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if tt.wantType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tt.wantType) {
				t.Fatalf("expected content type containing %q, got %q", tt.wantType, rec.Header().Get("Content-Type"))
			}
			for _, part := range tt.wantBodyParts {
				if !strings.Contains(rec.Body.String(), part) {
					t.Fatalf("expected response body to contain %q, got %q", part, rec.Body.String())
				}
			}
			for _, part := range tt.wantAbsent {
				if strings.Contains(rec.Body.String(), part) {
					t.Fatalf("expected response body not to contain %q, got %q", part, rec.Body.String())
				}
			}
		})
	}
}

type hijackableResponseWriter struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (w *hijackableResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *hijackableResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *hijackableResponseWriter) WriteHeader(status int) {}

func (w *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.rw, nil
}
