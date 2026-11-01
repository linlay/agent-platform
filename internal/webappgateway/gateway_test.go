package webappgateway

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
)

func gatewayFixture(t *testing.T) (*Gateway, *webapp.Service, webapp.Entry) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose hosting")
	}
	root := t.TempDir()
	cfg := config.DefaultWebAppsConfig()
	cfg.PlatformOrigin = "https://demo.platform.example.com"
	cfg.ContentDomain = "apps.example.net"
	cfg.InstanceSlug = "demo"
	s, err := webapp.Open(webapp.Options{Root: filepath.Join(root, "webapps"), DataRoot: filepath.Join(root, "data"), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(p, 0700)
			}
			return err
		})
	})
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, value := range map[string]string{
		"webapp-0123456789abcdef/webapp.json":         `{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"static-app","label":"Static app","version":"1.0.0","target":"linux-x64","appConfig":{"publicValue":"hello"},"userConfig":{"fields":[{"type":"text","name":"title","label":"Title","maxLength":64,"default":"hello"}]},"frontend":{"root":"frontend","index":"index.html","routeConfig":{"navigationFallback":"index.html"}}}`,
		"webapp-0123456789abcdef/frontend/index.html": "<!doctype html><h1>Private app</h1>", "webapp-0123456789abcdef/backend/private.txt": "private backend file",
	} {
		f, _ := z.Create(name)
		_, _ = f.Write([]byte(value))
	}
	z.Close()
	u, err := s.Upload(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.Install(context.Background(), webapp.InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	entry, err = s.Start(context.Background(), entry.ID, webapp.StateRequest{ExpectedRevision: entry.Revision, ExpectedStateRevision: entry.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	g := New(s, cfg)
	t.Cleanup(g.Close)
	if err := g.ensureListener(entry.ID); err != nil {
		t.Fatal(err)
	}
	return g, s, entry
}
func (g *Gateway) testRequest(t *testing.T, method, path string, cookie *http.Cookie, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, g.Origin("webapp-0123456789abcdef")+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func exchangeCookie(t *testing.T, g *Gateway) *http.Cookie {
	t.Helper()
	launch, err := g.Launch("owner", "webapp-0123456789abcdef", "/")
	if err != nil {
		t.Fatal(err)
	}
	w := g.testRequest(t, "POST", "/__desktop/session/exchange", nil, g.config.PlatformOrigin, url.Values{"ticket": {launch.Ticket}}.Encode())
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("session cookie missing")
	}
	return cookies[0]
}
func TestLaunchUsesOneTimePOSTAndHostOnlyPrivateCookie(t *testing.T) {
	g, _, entry := gatewayFixture(t)
	launch, err := g.Launch("owner", entry.ID, "/screen?mode=one")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(launch.Action, launch.Ticket) || !strings.HasSuffix(launch.Action, "/__desktop/session/exchange") {
		t.Fatal("ticket leaked into URL")
	}
	body := url.Values{"ticket": {launch.Ticket}}.Encode()
	if w := g.testRequest(t, "POST", "/__desktop/session/exchange", nil, "https://evil.example", body); w.Code != 403 {
		t.Fatal("exchange origin bypass")
	}
	w := g.testRequest(t, "POST", "/__desktop/session/exchange", nil, g.config.PlatformOrigin, body)
	if w.Code != 303 || w.Header().Get("Location") != "/screen?mode=one" {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	c := cookies[0]
	if c.Name != sessionCookieFor(entry.ID) || c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe session cookie %+v", c)
	}
	if w := g.testRequest(t, "POST", "/__desktop/session/exchange", nil, g.config.PlatformOrigin, body); w.Code != 403 {
		t.Fatal("ticket replay accepted")
	}
	for _, destination := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/%2f%2fevil.example", "/../admin", "/page#fragment"} {
		if _, err := g.Launch("owner", entry.ID, destination); err == nil {
			t.Fatalf("unsafe return path %s", destination)
		}
	}
}
func TestPrivateStaticPathsNeverFallThroughToPlatformOrBackend(t *testing.T) {
	g, _, _ := gatewayFixture(t)
	if w := g.testRequest(t, "GET", "/", nil, "", ""); w.Code != 401 {
		t.Fatal("unauthenticated content exposed")
	}
	cookie := exchangeCookie(t, g)
	for _, test := range []struct {
		path   string
		status int
	}{{"/", 200}, {"/webapp.json", 404}, {"/backend/private.txt", 404}, {"/api/admin/agents", 404}, {"/__desktop/unknown", 404}, {"/assets/missing.js", 404}, {"/%2e%2e/backend/private.txt", 400}} {
		w := g.testRequest(t, "GET", test.path, cookie, "", "")
		if w.Code != test.status {
			t.Fatalf("%s status=%d body=%s", test.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private backend file") {
			t.Fatal("private file exposed")
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing response headers")
		}
	}
	r := httptest.NewRequest("GET", g.Origin("webapp-0123456789abcdef")+"/client/route", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Private app") {
		t.Fatal("SPA navigation did not resolve")
	}
	for _, p := range []string{"/__desktop/bootstrap.json", "/__desktop/app-config.json", "/__desktop/user-config.json"} {
		w := g.testRequest(t, "GET", p, cookie, "", "")
		if w.Code != 200 {
			t.Fatal(p, w.Code)
		}
		if strings.Contains(w.Body.String(), cookie.Value) {
			t.Fatal("session credential exposed")
		}
	}
}
func TestBusinessWritesAndWebSocketsRequireExactOrigin(t *testing.T) {
	g, _, _ := gatewayFixture(t)
	cookie := exchangeCookie(t, g)
	for _, origin := range []string{"", "https://other.apps.example.net", g.config.PlatformOrigin} {
		w := g.testRequest(t, "POST", "/api/action", cookie, origin, "")
		if w.Code != 403 {
			t.Fatal("cross-origin write accepted")
		}
	}
	if w := g.testRequest(t, "POST", "/api/action", cookie, g.Origin("webapp-0123456789abcdef"), ""); w.Code != 405 {
		t.Fatal("same-origin request did not reach application method check")
	}
	r := httptest.NewRequest("GET", g.Origin("webapp-0123456789abcdef")+"/socket", nil)
	r.AddCookie(cookie)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Origin", "https://other.apps.example.net")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("WS origin bypass")
	}
}
func TestStopRevokesSessionsAndOldGenerationCannotRegainAccess(t *testing.T) {
	g, s, entry := gatewayFixture(t)
	cookie := exchangeCookie(t, g)
	stopped, err := s.Stop(context.Background(), entry.ID, webapp.StateRequest{ExpectedRevision: entry.Revision, ExpectedStateRevision: entry.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	if w := g.testRequest(t, "GET", "/", cookie, "", ""); w.Code != 503 {
		t.Fatal("stopped app still served content")
	}
	if _, err := s.Start(context.Background(), entry.ID, webapp.StateRequest{ExpectedRevision: stopped.Revision, ExpectedStateRevision: stopped.StateRevision}); err != nil {
		t.Fatal(err)
	}
	if w := g.testRequest(t, "GET", "/", cookie, "", ""); w.Code != 401 {
		t.Fatal("old session survived generation change")
	}
	fresh := exchangeCookie(t, g)
	if w := g.testRequest(t, "GET", "/", fresh, "", ""); w.Code != 200 {
		t.Fatal("new generation cannot open")
	}
	g.RevokeSessions()
	if w := g.testRequest(t, "GET", "/", fresh, "", ""); w.Code != 401 {
		t.Fatal("owner logout failed to revoke sessions")
	}
}
func TestTicketSessionExpiryAndCrossApplicationBinding(t *testing.T) {
	a := newAuthorization()
	now := time.Now()
	a.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := binding{appID: "one", generation: "generation", policy: "policy", owner: "owner", ctx: ctx}
	raw, _, _ := a.issue(b, "/")
	other := b
	other.appID = "two"
	if _, _, _, ok := a.exchange(raw, other); ok {
		t.Fatal("cross-app ticket accepted")
	}
	raw, _, _ = a.issue(b, "/")
	now = now.Add(61 * time.Second)
	if _, _, _, ok := a.exchange(raw, b); ok {
		t.Fatal("expired ticket accepted")
	}
	raw, _, _ = a.issue(b, "/")
	credential, _, _, ok := a.exchange(raw, b)
	if !ok {
		t.Fatal("exchange failed")
	}
	if _, ok := a.authenticate(credential, other); ok {
		t.Fatal("cross-app session accepted")
	}
	now = now.Add(31 * time.Minute)
	if _, ok := a.authenticate(credential, b); ok {
		t.Fatal("idle session remained valid")
	}
	raw, _, _ = a.issue(b, "/")
	credential, _, _, _ = a.exchange(raw, b)
	for i := 0; i < 17; i++ {
		now = now.Add(29 * time.Minute)
		_, ok = a.authenticate(credential, b)
	}
	if ok {
		t.Fatal("session exceeded absolute lifetime")
	}
}

func TestLocalPortDoesNotAcceptForgedHostOrForwardedIdentity(t *testing.T) {
	g, _, e := gatewayFixture(t)
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest("GET", g.Origin(e.ID)+"/", nil)
	r.Host = "demo.platform.example.com"
	r.Header.Set("X-Forwarded-Host", strings.TrimPrefix(g.Origin(e.ID), "http://"))
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("forged host reached gateway")
	}
	r, _ = http.NewRequest("GET", g.Origin(e.ID)+"/api/admin/agents", nil)
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("platform API fallback or anonymous content")
	}
}
