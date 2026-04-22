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
