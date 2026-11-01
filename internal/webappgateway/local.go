package webappgateway

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"agent-platform/internal/webapp"
)

type localListener struct {
	host     string
	server   *http.Server
	listener net.Listener
}

// In Compose, 0.0.0.0 is an explicit container-only choice: compose.webapps.yml
// publishes this range on host 127.0.0.1. The browser origin is always loopback.
func (g *Gateway) ensureListener(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return &webapp.Error{Code: "webapp_host_stopped", Status: 503}
	}
	if g.listeners[id] != nil {
		return nil
	}
	// Stopped apps retain their port; uninstalled apps release it before reuse.
	for key, l := range g.listeners {
		if _, err := g.service.Get(key); err != nil {
			l.server.Close()
			delete(g.listeners, key)
		}
	}
	for port := g.config.Frontend.PortStart; port <= g.config.Frontend.PortEnd; port++ {
		host := "127.0.0.1:" + strconv.Itoa(port)
		used := false
		for _, l := range g.listeners {
			if l.host == host {
				used = true
				break
			}
		}
		if used {
			continue
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort(g.config.Frontend.ListenAddress, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != host {
				http.NotFound(w, r)
				return
			}
			// A Host belonging to another listener cannot switch this socket's app.
			g.ServeHTTP(w, r)
		})
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		g.listeners[id] = &localListener{host: host, server: server, listener: listener}
		go func() { _ = server.Serve(listener) }()
		return nil
	}
	return &webapp.Error{Code: "webapp_frontend_ports_exhausted", Status: 503}
}

func (g *Gateway) Close() {
	g.mu.Lock()
	g.closed = true
	listeners := g.listeners
	g.listeners = map[string]*localListener{}
	g.mu.Unlock()
	g.RevokeSessions()
	for _, l := range listeners {
		_ = l.server.Close()
	}
}
