package webappbridge

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/webapp"
	"agent-platform/internal/webappgateway"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixture(t *testing.T) (*webapp.Service, webapp.Entry) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose runtime")
	}
	root := t.TempDir()
	cfg := config.DefaultWebAppsConfig()
	s, err := webapp.Open(webapp.Options{Root: filepath.Join(root, "webapps"), DataRoot: filepath.Join(root, "data"), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return err
		})
	})
	e := installFixture(t, s, "1.0.0", "")
	return s, e
}
func installFixture(t *testing.T, s *webapp.Service, version, expected string) webapp.Entry {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, value := range map[string]string{"webapp.json": `{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"bridge-app","label":"Bridge","version":"` + version + `","target":"linux-x64","appConfig":{"hello":"world"},"frontend":{"root":"frontend","index":"index.html"}}`, "frontend/index.html": "<h1>bridge</h1>"} {
		f, _ := z.Create("webapp-0123456789abcdef/" + name)
		f.Write([]byte(value))
	}
	z.Close()
	u, err := s.Upload(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Install(context.Background(), webapp.InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: expected})
	if err != nil {
		t.Fatal(err)
	}
	if e.DesiredState != "running" {
		e, err = s.Start(context.Background(), e.ID, webapp.StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
		if err != nil {
			t.Fatal(err)
		}
	}
	return e
}
func call(t *testing.T, b *Service, s *webapp.Service, id, action string, args any) *httptest.ResponseRecorder {
	t.Helper()
	scope, err := s.Acquire(id)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Release()
	body, _ := json.Marshal(map[string]any{"action": action, "args": args})
	r := httptest.NewRequest("POST", "http://app/__desktop/actions/call", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r, scope)
	return w
}
func TestConnectorScopeKeepsDurableReceiptAcrossGenerationAndUpdate(t *testing.T) {
	s, e := fixture(t)
	var count atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		n := count.Add(1)
		return &sdk.CallToolResult{StructuredContent: map[string]any{"count": n}}, nil
	})
	server := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer server.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	for name, value := range map[string]any{"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "none"}, "mcp.json": map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL}}}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	auth := connectorauth.New(context.Background(), sources, nil)
	bridge := New(Options{Apps: s, Owner: "owner", Connectors: &connectorops.Service{Sources: sources, Auth: auth}})
	defer bridge.Close()
	input := map[string]any{"connectorId": "demo", "adapter": "mcp", "component": "main", "toolName": "write", "arguments": map[string]any{}, "idempotencyKey": "same-call-001"}
	if w := call(t, bridge, s, e.ID, "connector.invoke", input); w.Code != 403 {
		t.Fatal("implicit grant", w.Code, w.Body.String())
	}
	c, _ := s.Configuration(e.ID)
	c, err := s.SavePolicy(e.ID, webapp.PolicyRequest{ExpectedRevision: c.Policy.Revision, Connectors: []webapp.ConnectorScope{{ConnectorID: "demo", Adapter: "mcp"}}})
	if err != nil {
		t.Fatal(err)
	}
	e = c.Entry
	for attempt := 0; attempt < 2; attempt++ {
		w := call(t, bridge, s, e.ID, "connector.invoke", input)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if count.Load() != 1 {
			t.Fatal("side effect replayed")
		}
		if attempt == 0 {
			e = installFixture(t, s, "2.0.0", e.Revision)
		}
	}
	bad := map[string]any{}
	for key, value := range input {
		bad[key] = value
	}
	bad["deviceId"] = "forged"
	if w := call(t, bridge, s, e.ID, "connector.invoke", bad); w.Code != 400 {
		t.Fatal("accepted identity override")
	}
	bad = map[string]any{}
	for key, value := range input {
		bad[key] = value
	}
	bad["arguments"] = map[string]any{"changed": true}
	if w := call(t, bridge, s, e.ID, "connector.invoke", bad); w.Code != 409 || !strings.Contains(w.Body.String(), "idempotency_conflict") {
		t.Fatal("changed side effect", w.Code, w.Body.String())
	}
}
func TestBackendCredentialsExpireAndCannotCrossGenerationOrTransport(t *testing.T) {
	s, e := fixture(t)
	b := New(Options{Apps: s, Owner: "owner"})
	defer b.Close()
	if err := b.ListenInternal(); err != nil {
		t.Fatal(err)
	}
	scope, _ := s.Acquire(e.ID)
	scope.Release()
	url, bootstrap, revoke, err := b.GrantBackend(webapp.RuntimeSpec{ID: e.ID, Generation: scope.Generation, Policy: scope.Policy, Context: scope.Context, Check: scope.Check})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	client := &http.Client{Timeout: time.Second}
	request := func(path, token, origin string) *http.Response {
		r, _ := http.NewRequest("POST", url+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request("/session", bootstrap, "http://127.0.0.1:20200")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("browser entered backend channel")
	}
	response = request("/session", bootstrap, "")
	var session struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(session.Token, "wba_") {
		t.Fatal("token missing")
	}
	if _, err := b.credentials.resolve(bootstrap); err == nil {
		t.Fatal("bootstrap used as action token")
	}
	if _, err := b.credentials.resolve(session.Token); err != nil {
		t.Fatal(err)
	}
	b.credentials.mu.Lock()
	b.credentials.now = func() time.Time { return time.UnixMilli(session.ExpiresAt + 1) }
	b.credentials.mu.Unlock()
	if _, err := b.credentials.resolve(session.Token); err == nil {
		t.Fatal("expired action token")
	}
	c, _ := s.Configuration(e.ID)
	if _, err := s.SavePolicy(e.ID, webapp.PolicyRequest{ExpectedRevision: c.Policy.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.credentials.mint(bootstrap); err == nil {
		t.Fatal("old bootstrap survived generation change")
	}
}
func TestPageBridgeRequiresCookieOriginAndCSRFAndMatchesSDKConfigShape(t *testing.T) {
	s, e := fixture(t)
	b := New(Options{Apps: s, Owner: "owner"})
	defer b.Close()
	cfg := config.DefaultWebAppsConfig()
	cfg.PlatformOrigin = "http://127.0.0.1:11948"
	g := webappgateway.New(s, cfg)
	g.Actions = b
	defer g.Close()
	launch, err := g.Launch("owner", e.ID, "/")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", launch.Action, strings.NewReader("ticket="+launch.Ticket))
	r.Header.Set("Origin", cfg.PlatformOrigin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	cookie := w.Result().Cookies()[0]
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", g.Origin(e.ID)+path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	bootstrap := get("/__desktop/bootstrap.json")
	var csrf struct {
		Token string `json:"csrfToken"`
	}
	json.Unmarshal(bootstrap.Body.Bytes(), &csrf)
	for _, path := range []string{"/__desktop/app-config.json", "/__desktop/user-config.json", "/__desktop/bridge.js"} {
		w := get(path)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if path == "/__desktop/app-config.json" && !strings.Contains(w.Body.String(), `"appConfig"`) {
			t.Fatal("configuration shape")
		}
		if path == "/__desktop/user-config.json" && !strings.Contains(w.Body.String(), `"values"`) {
			t.Fatal("user configuration shape")
		}
	}
	for _, token := range []string{"", csrf.Token} {
		r := httptest.NewRequest("POST", g.Origin(e.ID)+"/__desktop/actions/call", strings.NewReader(`{"action":"desktop.capabilities.list","args":{}}`))
		r.AddCookie(cookie)
		r.Header.Set("Origin", g.Origin(e.ID))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-WebApp-CSRF", token)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if token == "" && w.Code != 403 || token != "" && w.Code != 200 {
			t.Fatal("CSRF", w.Code, w.Body.String())
		}
	}
	w = call(t, b, s, e.ID, "desktop.native.dialog.selectFiles", map[string]any{})
	raw, _ := io.ReadAll(w.Result().Body)
	if w.Code != 422 || !strings.Contains(string(raw), "not_supported") {
		t.Fatal("native capability falsely available")
	}
}

func TestBridgeShutdownCancelsInFlightActions(t *testing.T) {
	s, e := fixture(t)
	began := make(chan struct{})
	finished := make(chan struct{})
	b := New(Options{Apps: s, Owner: "owner", Actions: map[string]Handler{"fixture.wait": func(ctx context.Context, _ *webapp.Scope, _ json.RawMessage) (any, error) {
		close(began)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	}}})
	scope, err := s.Acquire(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Release()
	r := httptest.NewRequest("POST", "http://app/__desktop/actions/call", strings.NewReader(`{"action":"fixture.wait","args":{}}`))
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { defer close(done); b.ServeHTTP(httptest.NewRecorder(), r, scope) }()
	<-began
	b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge shutdown retained action")
	}
	select {
	case <-finished:
	default:
		t.Fatal("action not cancelled")
	}
}

func TestSDKRequestsRejectIdentityEnvelopesAndUnknownFields(t *testing.T) {
	s, e := fixture(t)
	b := New(Options{Apps: s, Owner: "owner"})
	defer b.Close()
	scope, _ := s.Acquire(e.ID)
	defer scope.Release()
	for _, body := range []string{`{"action":"desktop.capabilities.list","args":{},"owner":"forged"}`, `{"Action":"desktop.capabilities.list","args":{}}`, `{"action":"desktop.capabilities.list","action":"connector.list","args":{}}`, `{"action":"desktop.app.getConfig","args":{"deviceId":"forged"}}`} {
		r := httptest.NewRequest("POST", "http://app/__desktop/actions/call", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r, scope)
		if w.Code != 400 {
			t.Fatal("unsafe SDK envelope", w.Code, w.Body.String())
		}
	}
}
