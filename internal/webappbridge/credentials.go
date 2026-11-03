package webappbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/webapp"
)

type credentialBinding struct {
	appID, generation, policy string
	ctx                       context.Context
	check                     func() error
	stop                      func() bool
}
type actionCredential struct {
	binding *credentialBinding
	expires time.Time
}
type credentials struct {
	closed    bool
	mu        sync.Mutex
	bootstrap map[[32]byte]*credentialBinding
	actions   map[[32]byte]actionCredential
	now       func() time.Time
}

func newCredentials() *credentials {
	return &credentials{bootstrap: map[[32]byte]*credentialBinding{}, actions: map[[32]byte]actionCredential{}, now: time.Now}
}
func randomToken(prefix string) string { return prefix + rand.Text() + rand.Text() }
func (c *credentials) collect() {
	now := c.now()
	for key, value := range c.actions {
		if !now.Before(value.expires) || value.binding.ctx.Err() != nil {
			delete(c.actions, key)
		}
	}
}
func (c *credentials) revoke(binding *credentialBinding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if binding.stop != nil {
		binding.stop()
	}
	for key, value := range c.bootstrap {
		if value == binding {
			delete(c.bootstrap, key)
		}
	}
	for key, value := range c.actions {
		if value.binding == binding {
			delete(c.actions, key)
		}
	}
}
func (s *Service) GrantBackend(spec webapp.RuntimeSpec) (string, string, func(), error) {
	if spec.Check == nil || spec.Check() != nil {
		return "", "", nil, sdkError("app_grant_required", 403)
	}
	s.mu.Lock()
	host := s.internalHost
	s.mu.Unlock()
	if host == "" {
		return "", "", nil, sdkError("webapp_bridge_unavailable", 503)
	}
	c := s.credentials
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collect()
	if c.closed || len(c.bootstrap) >= 128 || spec.Context.Err() != nil {
		return "", "", nil, sdkError("webapp_bridge_capacity", 429)
	}
	value := randomToken("wbb_")
	binding := &credentialBinding{appID: spec.ID, generation: spec.Generation, policy: spec.Policy.Revision, ctx: spec.Context, check: spec.Check}
	binding.stop = context.AfterFunc(spec.Context, func() { c.revoke(binding) })
	c.bootstrap[sha256.Sum256([]byte(value))] = binding
	return "http://" + host + "/webapps", value, func() { c.revoke(binding) }, nil
}
func (c *credentials) mint(raw string) (string, int64, error) {
	c.mu.Lock()
	c.collect()
	binding := c.bootstrap[sha256.Sum256([]byte(raw))]
	c.mu.Unlock()
	if binding == nil || binding.ctx.Err() != nil || binding.check() != nil {
		return "", 0, sdkError("app_grant_required", 403)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || len(c.actions) >= 1024 || binding.ctx.Err() != nil {
		return "", 0, sdkError("webapp_bridge_capacity", 429)
	}
	token := randomToken("wba_")
	expires := c.now().Add(10 * time.Minute)
	c.actions[sha256.Sum256([]byte(token))] = actionCredential{binding: binding, expires: expires}
	return token, expires.UnixMilli(), nil
}
func (c *credentials) resolve(raw string) (*credentialBinding, error) {
	c.mu.Lock()
	c.collect()
	value, ok := c.actions[sha256.Sum256([]byte(raw))]
	if c.closed {
		ok = false
	}
	c.mu.Unlock()
	if !ok || value.binding.ctx.Err() != nil || value.binding.check() != nil {
		return nil, sdkError("app_grant_required", 403)
	}
	return value.binding, nil
}
func (s *Service) ListenInternal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sdkError("webapp_bridge_unavailable", 503)
	}
	if s.internal != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.internalHost = listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(s.serveInternal), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.internal = server
	go func() { _ = server.Serve(listener) }()
	return nil
}
func (s *Service) serveInternal(w http.ResponseWriter, r *http.Request) {
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	host := s.internalHost
	s.mu.Unlock()
	if net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() || r.Host != host || r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || len(r.Header.Values("Authorization")) != 1 {
		response(w, "", nil, sdkError("app_grant_required", 403))
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		response(w, "", nil, sdkError("app_grant_required", 403))
		return
	}
	raw := strings.TrimPrefix(authorization, "Bearer ")
	if r.URL.Path == "/webapps/session" {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			response(w, "", nil, sdkError("invalid_args", 400))
			return
		}
		token, expiry, err := s.credentials.mint(raw)
		if err != nil {
			response(w, "", nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"token":"` + token + `","expiresAt":` + strconv.FormatInt(expiry, 10) + `}`))
		return
	}
	if r.URL.Path != "/webapps/actions/call" {
		http.NotFound(w, r)
		return
	}
	binding, err := s.credentials.resolve(raw)
	if err != nil {
		response(w, "", nil, err)
		return
	}
	scope, err := s.apps.AcquireInstance(binding.appID, binding.generation, binding.policy)
	if err != nil {
		response(w, "", nil, err)
		return
	}
	defer scope.Release()
	s.serve(w, r, scope, true)
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	server := s.internal
	s.internal = nil
	s.internalHost = ""
	s.mu.Unlock()
	if server != nil {
		server.Close()
	}
	c := s.credentials
	c.mu.Lock()
	c.closed = true
	bindings := []*credentialBinding{}
	for _, binding := range c.bootstrap {
		bindings = append(bindings, binding)
	}
	c.mu.Unlock()
	for _, binding := range bindings {
		c.revoke(binding)
	}
}
