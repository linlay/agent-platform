package webappgateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/webapp"
)

func backendPath(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
func hostCookie(name string) bool {
	return name == "access_token" || name == "__Host-ap-webapp" || strings.HasPrefix(name, "ap_webapp_") || strings.HasPrefix(name, "__Host-ap-")
}
func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request, scope *webapp.Scope) {
	host, port, err := net.SplitHostPort(scope.BackendAddress)
	if err != nil || host != "127.0.0.1" || port == "" {
		writeError(w, &webapp.Error{Code: "webapp_backend_unavailable", Status: 503})
		return
	}
	target := &url.URL{Scheme: "http", Host: scope.BackendAddress}
	// Upgrade tunnels need explicit connection cancellation: ReverseProxy's
	// bidirectional copy can outlive request.Context after the 101 handshake.
	var connectionsMu sync.Mutex
	connections := map[net.Conn]bool{}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != scope.BackendAddress {
			return nil, &webapp.Error{Code: "webapp_backend_target_invalid", Status: 503}
		}
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", scope.BackendAddress)
		if err == nil {
			connectionsMu.Lock()
			connections[conn] = true
			connectionsMu.Unlock()
			if scope.Context.Err() != nil {
				conn.Close()
				return nil, scope.Context.Err()
			}
		}
		return conn, err
	}}
	closed := make(chan struct{})
	stop := context.AfterFunc(scope.Context, func() {
		defer close(closed)
		connectionsMu.Lock()
		defer connectionsMu.Unlock()
		for conn := range connections {
			conn.Close()
		}
	})
	defer func() {
		if !stop() {
			<-closed
		}
		transport.CloseIdleConnections()
		connectionsMu.Lock()
		for conn := range connections {
			conn.Close()
		}
		connectionsMu.Unlock()
	}()
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: func(request *httputil.ProxyRequest) {
		request.SetURL(target)
		request.Out.Host = scope.BackendAddress
		for name := range request.Out.Header {
			lower := strings.ToLower(name)
			if lower == "forwarded" || lower == "proxy-authorization" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-ap-") || strings.HasPrefix(lower, "x-agent-") || strings.HasPrefix(lower, "x-webapp-") {
				request.Out.Header.Del(name)
			}
		}
		cookies := request.Out.Cookies()
		request.Out.Header.Del("Cookie")
		for _, cookie := range cookies {
			if !hostCookie(cookie.Name) {
				request.Out.AddCookie(cookie)
			}
		}
		if g.PlatformAuthorization != nil && g.PlatformAuthorization(request.Out.Header.Get("Authorization")) {
			request.Out.Header.Del("Authorization")
		}
	}, ModifyResponse: func(response *http.Response) error {
		cookies := response.Cookies()
		response.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			if !hostCookie(cookie.Name) && cookie.Domain == "" {
				response.Header.Add("Set-Cookie", cookie.String())
			}
		}
		if location, err := response.Location(); err == nil && location.Host == scope.BackendAddress {
			location.Scheme = "http"
			origin, _ := url.Parse(g.Origin(scope.Entry.ID))
			location.Host = origin.Host
			response.Header.Set("Location", location.String())
		}
		response.Header.Set("Cache-Control", "no-store")
		response.Header.Set("X-Content-Type-Options", "nosniff")
		response.Header.Set("Referrer-Policy", "no-referrer")
		response.Header.Set("Content-Security-Policy", "object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		return scope.Check()
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		writeError(w, &webapp.Error{Code: "webapp_backend_unavailable", Status: 503})
	}}
	proxy.ServeHTTP(w, r)
}
