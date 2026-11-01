package webappgateway

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
)

type Gateway struct {
	service   *webapp.Service
	config    config.WebAppsConfig
	auth      *authorization
	mu        sync.Mutex
	listeners map[string]*localListener
	closed    bool
}
type Launch struct {
	Action    string `json:"action"`
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expiresAt"`
}

func New(service *webapp.Service, cfg config.WebAppsConfig) *Gateway {
	return &Gateway{service: service, config: cfg, auth: newAuthorization(), listeners: map[string]*localListener{}}
}

// Local ports are distinct origins. Host never selects an app on Platform's mux.
func (g *Gateway) HostID(host string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, l := range g.listeners {
		if host == l.host {
			return id
		}
	}
	return ""
}
func (g *Gateway) Origin(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if l := g.listeners[id]; l != nil {
		return "http://" + l.host
	}
	return ""
}
func sessionCookieFor(id string) string { return "ap_webapp_" + strings.TrimPrefix(id, "webapp-") }
func (g *Gateway) RevokeSessions()      { g.auth.revokeAll() }
func (g *Gateway) Launch(owner, id, destination string) (Launch, error) {
	if owner == "" {
		return Launch{}, &webapp.Error{Code: "webapp_owner_required", Status: 403}
	}
	if destination == "" {
		destination = "/"
	}
	u, err := url.Parse(destination)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || !safeNavigation(u.Path) {
		return Launch{}, &webapp.Error{Code: "webapp_return_path_invalid", Status: 400}
	}
	scope, err := g.service.Acquire(id)
	if err != nil {
		return Launch{}, err
	}
	defer scope.Release()
	if err := scope.Check(); err != nil {
		return Launch{}, err
	}
	if err := g.ensureListener(id); err != nil {
		return Launch{}, err
	}
	b := binding{appID: id, generation: scope.Generation, policy: scope.Policy.Revision, owner: owner, ctx: scope.Context}
	raw, expires, ok := g.auth.issue(b, u.String())
	if !ok {
		return Launch{}, &webapp.Error{Code: "webapp_session_capacity", Status: 429}
	}
	return Launch{Action: g.Origin(id) + "/__desktop/session/exchange", Ticket: raw, ExpiresAt: expires.UnixMilli()}, nil
}
func safeNavigation(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.ContainsAny(p, "\\\r\n\x00") && (p == "/" || path.Clean(p) == strings.TrimSuffix(p, "/")) && utf8.ValidString(p)
}
func writeError(w http.ResponseWriter, err error) {
	code, status := webapp.ErrorCode(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": code}})
}
func jsonResult(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	id := g.HostID(r.Host)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	scope, err := g.service.Acquire(id)
	if err != nil {
		writeError(w, err)
		return
	}
	defer scope.Release()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	deadlineDone := make(chan struct{})
	stop := context.AfterFunc(scope.Context, func() {
		defer close(deadlineDone)
		cancel()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-deadlineDone
		}
	}()
	r = r.WithContext(ctx)
	if !safeNavigation(r.URL.Path) {
		writeError(w, &webapp.Error{Code: "webapp_path_invalid", Status: 400})
		return
	}
	b := binding{appID: id, generation: scope.Generation, policy: scope.Policy.Revision, ctx: scope.Context}
	if r.URL.Path == "/__desktop/session/exchange" {
		g.exchange(w, r, b, scope)
		return
	}
	values := []string{}
	for _, cookie := range r.Cookies() {
		if cookie.Name == sessionCookieFor(id) {
			values = append(values, cookie.Value)
		}
	}
	csrf := ""
	authenticated := false
	if len(values) == 1 {
		csrf, authenticated = g.auth.authenticate(values[0], b)
	}
	if !authenticated {
		if r.Method == "GET" && strings.Contains(r.Header.Get("Accept"), "text/html") && !strings.HasPrefix(r.URL.Path, "/__desktop") {
			http.Redirect(w, r, g.config.PlatformOrigin+"/webapps?open="+url.QueryEscape(id), http.StatusSeeOther)
			return
		}
		writeError(w, &webapp.Error{Code: "webapp_session_required", Status: 401})
		return
	}
	if err := scope.Check(); err != nil {
		writeError(w, err)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if r.Header.Get("Origin") != g.Origin(id) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, &webapp.Error{Code: "webapp_origin_not_allowed", Status: 403})
			return
		}
	}
	switch r.URL.Path {
	case "/__desktop/bootstrap.json":
		if r.Method != "GET" {
			break
		}
		jsonResult(w, map[string]any{"csrfToken": csrf})
		return
	case "/__desktop/app-config.json":
		if r.Method != "GET" {
			break
		}
		jsonResult(w, scope.Manifest.AppConfig)
		return
	case "/__desktop/user-config.json":
		if r.Method != "GET" {
			break
		}
		jsonResult(w, scope.UserConfig)
		return
	}
	if r.URL.Path == "/__desktop" || strings.HasPrefix(r.URL.Path, "/__desktop/") {
		http.NotFound(w, r)
		return
	}
	g.serveStatic(w, r, scope)
}
func (g *Gateway) exchange(w http.ResponseWriter, r *http.Request, b binding, scope *webapp.Scope) {
	if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Origin") != g.config.PlatformOrigin {
		writeError(w, &webapp.Error{Code: "webapp_ticket_invalid", Status: 403})
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/x-www-form-urlencoded" {
		writeError(w, &webapp.Error{Code: "webapp_ticket_invalid", Status: 400})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		writeError(w, &webapp.Error{Code: "webapp_ticket_invalid", Status: 400})
		return
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil || len(form) != 1 || len(form["ticket"]) != 1 {
		writeError(w, &webapp.Error{Code: "webapp_ticket_invalid", Status: 400})
		return
	}
	value, session, destination, ok := g.auth.exchange(form.Get("ticket"), b)
	if !ok {
		writeError(w, &webapp.Error{Code: "webapp_ticket_invalid", Status: 403})
		return
	}
	if err := scope.Check(); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieFor(b.appID), Value: value, Path: "/", Secure: false, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: session.created.Add(8 * time.Hour), MaxAge: int((8 * time.Hour).Seconds())})
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
func (g *Gateway) serveStatic(w http.ResponseWriter, r *http.Request, scope *webapp.Scope) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = scope.Manifest.Frontend.Index
	}
	f, err := scope.CodeFile(name)
	if err != nil && scope.Manifest.Frontend.RouteConfig.NavigationFallback != "" && path.Ext(name) == "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
		name = scope.Manifest.Frontend.RouteConfig.NavigationFallback
		f, err = scope.CodeFile(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, fi.ModTime(), &contextFile{ReadSeeker: f, ctx: r.Context()})
}

type contextFile struct {
	io.ReadSeeker
	ctx context.Context
}

func (f *contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.ReadSeeker.Read(p)
}
