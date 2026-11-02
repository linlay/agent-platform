package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"golang.org/x/net/http/httpproxy"
)

func TestWebSocketDialerSharesProxyAndTLS(t *testing.T) {
	f, err := NewFactory(Config{Mode: "fixed", URL: "http://proxy.example:8080", Bypass: []string{"*.internal"}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.CloseIdleConnections()
	f.transport.base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "configured.example"}
	d, err := f.webSocketDialer(context.Background(), "wss://speech.example/task", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw     string
		proxied bool
	}{
		{"wss://speech.example/task", true}, {"ws://speech.example/task", true},
		{"wss://speech.internal/task", false}, {"ws://127.0.0.1/task", false},
	} {
		u, _ := url.Parse(tc.raw)
		selected, err := f.webSocketDialer(context.Background(), tc.raw, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var p *url.URL
		if selected.Proxy != nil {
			p, err = selected.Proxy(&http.Request{URL: u})
		}
		if err != nil || (p != nil) != tc.proxied {
			t.Fatalf("%s: %v %v", tc.raw, p, err)
		}
	}
	if d.HandshakeTimeout != time.Second || d.NetDialContext == nil || d.TLSClientConfig.ServerName != "configured.example" {
		t.Fatal("transport policy lost")
	}
	d.TLSClientConfig.ServerName = "caller.example"
	if f.transport.base.TLSClientConfig.ServerName != "configured.example" {
		t.Fatal("shared TLS configuration mutated")
	}
}

func TestWebSocketHTTPSProxyTunnelAndTLSVerification(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		kind, data, err := c.ReadMessage()
		if err == nil {
			_ = c.WriteMessage(kind, data)
		}
	}))
	defer upstream.Close()
	var connected atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "speech.example:80" || r.Header.Get("Proxy-Authorization") == "" {
			t.Error("CONNECT destination or proxy authentication lost")
			http.Error(w, "invalid", 400)
			return
		}
		connected.Add(1)
		target, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		defer target.Close()
		client, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(target, client); _ = target.Close(); close(done) }()
		_, _ = io.Copy(client, target)
		_ = client.Close()
		<-done
	}))
	defer proxy.Close()
	p, _ := url.Parse(proxy.URL)
	p.User = url.UserPassword("test", "proxy-secret")
	f, err := NewFactory(Config{Mode: "fixed", URL: p.String()})
	if err != nil {
		t.Fatal(err)
	}
	defer f.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c, _, err := f.DialWebSocket(ctx, "ws://speech.example/task", nil, time.Second); err == nil {
		c.Close()
		t.Fatal("untrusted proxy certificate accepted")
	}
	if connected.Load() != 0 {
		t.Fatal("untrusted TLS proxy reached CONNECT")
	}
	f.transport.base.TLSClientConfig = proxy.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	c, _, err := f.DialWebSocket(ctx, "ws://speech.example/task", nil, time.Second)
	if err != nil {
		t.Fatal("trusted HTTPS proxy connection failed")
	}
	defer c.Close()
	if err = c.WriteMessage(websocket.TextMessage, []byte("through proxy")); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.ReadMessage()
	if err != nil || string(data) != "through proxy" || connected.Load() != 1 {
		t.Fatalf("tunnel failed: %v", err)
	}
}

func TestWebSocketSOCKS5HUsesRemoteDNS(t *testing.T) {
	f, err := NewFactory(Config{Mode: "fixed", URL: "socks5h://proxy.example:1080"})
	if err != nil {
		t.Fatal(err)
	}
	defer f.CloseIdleConnections()
	d, err := f.webSocketDialer(context.Background(), "wss://speech.example/task", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Proxy(&http.Request{})
	if err != nil || p.Scheme != "socks5" {
		t.Fatal("Gorilla remote-DNS SOCKS5 adaptation missing")
	}
}

func TestWebSocketProxyFailureDoesNotFallBack(t *testing.T) {
	r, err := newResolver(Config{Mode: "pac_auto"}, httpproxy.Config{}, func(context.Context) (systemSettings, error) {
		return systemSettings{Auto: true, ResolveAuto: func(context.Context, *url.URL) (*url.URL, error) { return nil, errors.New("PAC unavailable") }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := factoryForResolver(r).webSocketDialer(context.Background(), "wss://speech.example/task", time.Second); err == nil || d != nil {
		t.Fatalf("%v %v", d, err)
	}
}
