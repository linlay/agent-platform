package httpclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// webSocketDialer resolves once per connection. Routing remains in Resolver;
// the small scheme adaptation accounts for Gorilla's HTTP/SOCKS5-only proxy API.
func (f *Factory) webSocketDialer(ctx context.Context, rawURL string, timeout time.Duration) (*websocket.Dialer, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	decision, err := f.resolver.Resolve(ctx, u)
	if err != nil {
		return nil, reportFailure(decision, "proxy-resolution", err)
	}
	t := f.transport.base
	d := &websocket.Dialer{HandshakeTimeout: timeout, NetDialContext: t.DialContext}
	if t.TLSClientConfig != nil {
		d.TLSClientConfig = t.TLSClientConfig.Clone()
	}
	if decision.Proxy != nil {
		p := *decision.Proxy
		// Gorilla's SOCKS5 sends hostnames to the proxy.
		if p.Scheme == "socks5h" {
			p.Scheme = "socks5"
		}
		if p.Scheme == "https" {
			proxyTLS := &tls.Config{ServerName: p.Hostname(), MinVersion: tls.VersionTLS12}
			if t.TLSClientConfig != nil {
				proxyTLS = t.TLSClientConfig.Clone()
				proxyTLS.ServerName = p.Hostname()
			}
			if p.Port() == "" {
				p.Host = net.JoinHostPort(p.Hostname(), "443")
			}
			p.Scheme = "http" // Gorilla performs CONNECT inside the verified TLS tunnel.
			d.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				dial := t.DialContext
				if dial == nil {
					dial = (&net.Dialer{}).DialContext
				}
				conn, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				secure := tls.Client(conn, proxyTLS)
				if err = secure.HandshakeContext(ctx); err != nil {
					_ = conn.Close()
					return nil, err
				}
				return secure, nil
			}
		}
		d.Proxy = func(*http.Request) (*url.URL, error) { return &p, nil }
	}
	return d, nil
}

func (f *Factory) DialWebSocket(ctx context.Context, rawURL string, header http.Header, timeout time.Duration) (*websocket.Conn, *http.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	d, err := f.webSocketDialer(ctx, rawURL, timeout)
	if err != nil {
		return nil, nil, err
	}
	return d.DialContext(ctx, rawURL, header)
}

func DialWebSocket(ctx context.Context, rawURL string, header http.Header, timeout time.Duration) (*websocket.Conn, *http.Response, error) {
	return processFactory.Load().DialWebSocket(ctx, rawURL, header, timeout)
}
