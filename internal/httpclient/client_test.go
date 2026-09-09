package httpclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"
)

func getBody(t *testing.T, c *http.Client, target string) string {
	t.Helper()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestHTTPProxyAndRefreshDuringStream(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "model.example.test" {
			t.Errorf("request did not use HTTP proxy format: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, "data: last\n\n")
		case <-r.Context().Done():
		}
	}))
	defer first.Close()
	defer finish()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "second proxy") }))
	defer second.Close()
	var selected atomic.Pointer[systemSettings]
	selected.Store(&systemSettings{HTTP: proxyURL(t, first.URL)})
	r, _ := newResolver(Config{}, httpproxy.Config{}, func(context.Context) (systemSettings, error) { return *selected.Load(), nil })
	f := factoryForResolver(r)
	defer f.CloseIdleConnections()
	c := f.NewClient(0)
	if c.Timeout != 0 || f.transport.base.ResponseHeaderTimeout != 0 {
		t.Fatal("stream acquired a total/header timeout")
	}
	resp, err := c.Get("http://model.example.test/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("%q %v", line, err)
	}
	selected.Store(&systemSettings{HTTP: proxyURL(t, second.URL)})
	f.Refresh()
	if got := getBody(t, c, "http://model.example.test/next"); got != "second proxy" {
		t.Fatal(got)
	}
	finish()
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "data: last") {
		t.Fatalf("refresh interrupted stream: %q %v", rest, err)
	}
}

func TestHTTPSConnectProxy(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secure upstream") }))
	defer upstream.Close()
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "example.com:443" {
			t.Errorf("invalid CONNECT %s %s", r.Method, r.Host)
			http.Error(w, "bad CONNECT", 400)
			return
		}
		connects.Add(1)
		remote, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			http.Error(w, "dial failed", 502)
			return
		}
		defer remote.Close()
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { io.Copy(remote, buffer); remote.Close() }()
		io.Copy(conn, remote)
	}))
	defer proxy.Close()
	f, err := NewFactory(Config{Mode: "fixed", URL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer f.CloseIdleConnections()
	// httptest's certificate contains example.com; trust only its certificate.
	f.transport.base.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	if got := getBody(t, f.NewClient(2*time.Second), "https://example.com/"); got != "secure upstream" {
		t.Fatal(got)
	}
	if connects.Load() != 1 {
		t.Fatalf("CONNECT calls %d", connects.Load())
	}
}

func TestSOCKSProxyRemoteDNS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		header := make([]byte, 2)
		if _, err = io.ReadFull(conn, header); err != nil {
			done <- err
			return
		}
		methods := make([]byte, int(header[1]))
		if _, err = io.ReadFull(conn, methods); err != nil {
			done <- err
			return
		}
		conn.Write([]byte{5, 0})
		request := make([]byte, 5)
		if _, err = io.ReadFull(conn, request); err != nil {
			done <- err
			return
		}
		if request[0] != 5 || request[1] != 1 || request[3] != 3 {
			done <- errors.New("expected SOCKS5 CONNECT with remote domain")
			return
		}
		address := make([]byte, int(request[4])+2)
		if _, err = io.ReadFull(conn, address); err != nil {
			done <- err
			return
		}
		if string(address[:len(address)-2]) != "does-not-resolve.invalid" {
			done <- errors.New("wrong SOCKS destination")
			return
		}
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			done <- err
			return
		}
		req.Body.Close()
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nsocks")
		done <- err
	}()
	f, err := NewFactory(Config{Mode: "fixed", URL: "socks5://" + ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer f.CloseIdleConnections()
	if got := getBody(t, f.NewClient(3*time.Second), "http://does-not-resolve.invalid/"); got != "socks" {
		t.Fatal(got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyFailureNeverDialsUpstream(t *testing.T) {
	f, err := NewFactory(Config{Mode: "fixed", URL: "http://user:secret@proxy.test:8080"})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.base.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "proxy.test:8080" {
			t.Errorf("unexpected fallback dial %s", address)
		}
		return nil, errors.New("dial failure including secret")
	}
	_, err = f.NewClient(time.Second).Get("https://upstream.test/")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user") {
		t.Fatalf("unsafe or missing error %v", err)
	}
	var routeErr *TransportError
	if !errors.As(err, &routeErr) || routeErr.Stage != "proxy-connect" || routeErr.Source != "explicit" {
		t.Fatalf("missing route diagnostics: %v", err)
	}
}

func TestDirectAndLoopbackNeverUseProxy(t *testing.T) {
	for _, c := range []*http.Client{DirectClient(time.Second), func() *http.Client {
		f, _ := NewFactory(Config{Mode: "fixed", URL: "http://broken.test:80"})
		t.Cleanup(f.CloseIdleConnections)
		return f.NewClient(time.Second)
	}()} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "local") }))
		if got := getBody(t, c, s.URL); got != "local" {
			t.Fatal(got)
		}
		s.Close()
	}
}

func TestCloneTransportPreservesTimeoutAndResolver(t *testing.T) {
	f, _ := NewFactory(Config{Mode: "fixed", URL: "proxy.test:80"})
	cloned := CloneTransport(f.transport, 23*time.Millisecond).(*Transport)
	if cloned == f.transport || cloned.base == f.transport.base || cloned.resolver != f.resolver {
		t.Fatal("incorrect clone")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cloned.base.DialContext(ctx, "tcp", "192.0.2.1:80"); err == nil {
		t.Fatal("dial ignored cancellation")
	}
	// TLS configuration and all non-connect settings survive the clone.
	f.transport.base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	cloned = CloneTransport(f.transport, 0).(*Transport)
	if cloned.base.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatal("lost TLS settings")
	}
}

func TestRequestDeadlineRemainsClassifiable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	f, _ := NewFactory(Config{Mode: "fixed", URL: s.URL})
	defer f.CloseIdleConnections()
	_, err := f.NewClient(30 * time.Millisecond).Get("http://example.test/")
	var timeout net.Error
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("deadline lost: %v", err)
	}
}
