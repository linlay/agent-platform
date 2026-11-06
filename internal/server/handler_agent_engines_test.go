package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/processgroup"
	runtimeacp "agent-platform/internal/runtime/acp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
)

func TestAgentEngineHTTPUsesDedicatedLocalAuthority(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bootstrap fixture; native Windows ACL validation remains T10")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	delivery := filepath.Join(root, "desktop", "data", "agent-engines")
	if err := os.MkdirAll(delivery, 0700); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 64)
	bootstrap := dto.ManagementBootstrap{SchemaVersion: 1, HostID: "host-a", LaunchNonce: "nonce", ManagementSecret: secret, DeliveryRoot: delivery}
	data, _ := json.Marshal(bootstrap)
	file := filepath.Join(root, "bootstrap.json")
	os.WriteFile(file, data, 0600)
	engines, err := agentengine.New(filepath.Join(root, "runtime"), file, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engines.Close()
	s := &Server{deps: Dependencies{AgentEngines: engines}, router: http.NewServeMux()}
	s.router.HandleFunc("/api/agent-engines", s.handleAgentEngineDirectory)
	call := func(method, path, body, token, host, epoch, remote, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("Authorization", token)
		r.Header.Set("X-ZenMind-Desktop-Host", host)
		r.Header.Set("X-ZenMind-Engine-Epoch", epoch)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	statusPath := "/api/desktop/agent-engines/status"
	for _, test := range []struct {
		token, host, remote, origin string
		want                        int
	}{
		{"Bearer ordinary-app-token", "host-a", "127.0.0.1:1234", "", 401},
		{"ZenMindEngine " + secret, "other", "127.0.0.1:1234", "", 403},
		{"ZenMindEngine " + secret, "host-a", "192.0.2.1:1234", "", 403},
		{"ZenMindEngine " + secret, "host-a", "127.0.0.1:1234", "http://localhost", 403},
	} {
		w := call("GET", statusPath, "", test.token, test.host, "", test.remote, test.origin)
		if w.Code != test.want {
			t.Fatalf("authorization %d: %s", w.Code, w.Body.String())
		}
	}
	w := call("GET", statusPath, "", "ZenMindEngine "+secret, "host-a", "", "127.0.0.1:1234", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Exercise the actual loopback HTTP transport as well as hostile handler inputs.
	hostServer := httptest.NewServer(s)
	defer hostServer.Close()
	request, err := http.NewRequest(http.MethodGet, hostServer.URL+statusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "ZenMindEngine "+secret)
	request.Header.Set("X-ZenMind-Desktop-Host", "host-a")
	response, err := hostServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("loopback transport rejected", response.StatusCode)
	}
	epoch := engines.Status().Epoch
	body := `{"apiVersion":1,"revision":1,"entries":[]}`
	w = call("PUT", "/api/desktop/agent-engines/snapshot", body, "ZenMindEngine "+secret, "host-a", "old", "127.0.0.1:1234", "")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("PUT", "/api/desktop/agent-engines/snapshot", body, "ZenMindEngine "+secret, "host-a", epoch, "127.0.0.1:1234", "")
	if w.Code != 200 || !engines.Status().Ready {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"apiVersion":1,"revision":2,"entries":[],"env":{"NODE_OPTIONS":"evil"}}`, `{"apiVersion":1,"revision":2,"entries":[],"entries":[]}`, strings.Repeat(" ", 1<<20) + `{}`} {
		w = call("PUT", "/api/desktop/agent-engines/snapshot", body, "ZenMindEngine "+secret, "host-a", epoch, "127.0.0.1:1234", "")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = call("GET", "/api/agent-engines", "", "", "", "", "127.0.0.1:1234", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), epoch) || strings.Contains(w.Body.String(), root) {
		t.Fatal("public directory leaked authority", w.Body.String())
	}
}

func TestEngineSessionHTTPChecksChatOwnerAndStrictContract(t *testing.T) {
	fixture := newTestFixture(t)
	root := t.TempDir()
	engines, err := agentengine.New(filepath.Join(root, "runtime"), "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engines.Close()
	host, err := runtimeacp.New(engines, filepath.Join(root, "runtime"), runtimeacp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	store := fixture.chats.(*chat.FileStore)
	if _, _, err = store.EnsureChatWithSource("private-engine", "project", "", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	fixture.server.deps.EngineSessions = runtimeacp.NewSessions(host, store, func(key string) (catalog.AgentDefinition, bool) {
		return catalog.AgentDefinition{Key: key, Engine: "acp", Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}, true
	})
	for _, tc := range []struct {
		subject, body string
		status        int
	}{
		{"bob", `{"chatId":"private-engine"}`, 403},
		{"alice", `{"chatId":"private-engine","engineId":"attacker/engine"}`, 400},
		{"alice", `{"chatId":"private-engine","chatId":"other"}`, 400},
		{"alice", `{"chatId":"private-engine"}`, 503},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/agent-engines/sessions/prepare", strings.NewReader(tc.body))
		request = request.WithContext(WithPrincipal(request.Context(), &Principal{Subject: tc.subject}))
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, request)
		if recorder.Code != tc.status {
			t.Fatalf("%s %s: status %d body %s", tc.subject, tc.body, recorder.Code, recorder.Body.String())
		}
	}
	if host.Stats().Processes != 0 {
		t.Fatal("owner/registry failure spawned process")
	}
}

func TestEngineAuthHTTPRejectsUnownedAndArbitraryLaunch(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		path, body string
		principal  bool
		want       int
	}{
		{"start", `{"engineId":"fixture/agent","methodId":"login"}`, false, 403},
		{"start", `{"engineId":"fixture/agent","methodId":"login","command":"evil"}`, true, 400},
		{"cancel", `{"authOperationId":"op","owner":"other"}`, true, 400},
		{"start", `{"engineId":"fixture/agent","methodId":"login"}`, true, 503},
	} {
		r := httptest.NewRequest("POST", "/api/agent-engines/auth/"+tc.path, strings.NewReader(tc.body))
		if tc.principal {
			r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: "alice", Claims: map[string]any{"deviceId": "desktop"}}))
		}
		w := httptest.NewRecorder()
		fixture.server.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestEnginePrepareReservesOwnedChatWithoutPrompt(t *testing.T) {
	engines, root := projectEngineFixture(t, `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);let result;if(m.method==='initialize')result={protocolVersion:1};else if(m.method==='session/new')result={sessionId:'first',configOptions:[{id:'model',name:'Model',type:'select',category:'model',currentValue:'test',options:[{value:'test',name:'Test model'}]}]};else if(m.method==='session/prompt'){setTimeout(()=>console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{stopReason:'end_turn'}})),1000);return;}else process.exit(99);console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result}));});`)
	fixture := newTestFixture(t)
	fixture.server.deps.AgentEngines = engines
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{"isProject": true, "definition": map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"engineId": "fixture/agent", "workspaceRoot": root}}})
	host, err := runtimeacp.New(engines, filepath.Join(root, "runtime"), runtimeacp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sessions := runtimeacp.NewSessions(host, fixture.chats.(*chat.FileStore), fixture.server.deps.Registry.AgentDefinition)
	defer sessions.Close()
	fixture.server.deps.EngineSessions = sessions
	for _, method := range []string{"GET", "POST", "GET"} {
		r := httptest.NewRequest(method, "/api/agent-engines/sessions/preview?agentKey="+created.Key, nil)
		w := httptest.NewRecorder()
		fixture.server.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("preview %s: %d %s", method, w.Code, w.Body.String())
		}
		if method == "POST" && !strings.Contains(w.Body.String(), "Test model") {
			t.Fatal(w.Body.String())
		}
		if strings.Contains(w.Body.String(), "preparationId") || strings.Contains(w.Body.String(), "chatId") {
			t.Fatal("preview leaked executable handle", w.Body.String())
		}
	}
	for _, tc := range []struct {
		subject, chatID string
		want            int
	}{{"alice", "prepared-first", 200}, {"bob", "prepared-first", 403}, {"", "prepared-first", 403}, {"alice", "prepared-first", 200}, {"", "prepared-public", 200}, {"", "prepared-public", 200}} {
		r := httptest.NewRequest("POST", "/api/agent-engines/sessions/prepare", strings.NewReader(string(encodeProjectEngine(t, map[string]any{"chatId": tc.chatID, "agentKey": created.Key}))))
		if tc.subject != "" {
			r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: tc.subject}))
		}
		w := httptest.NewRecorder()
		fixture.server.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.subject, w.Code, w.Body.String())
		}
	}
	for _, chatID := range []string{"prepared-first", "prepared-public"} {
		summary, err := fixture.chats.Summary(chatID)
		if err != nil || summary == nil || summary.LastRunID != "" {
			t.Fatal("prepare invoked query", summary, err)
		}
		if chatID == "prepared-public" && summary.Source != "" {
			t.Fatal("anonymous preparation must use the ordinary Query source", summary.Source)
		}
	}
	p, err := sessions.Inspect("prepared-first")
	if err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan error, 1)
	go func() {
		_, err := sessions.Prompt(t.Context(), p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hold")}, nil)
		promptDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for host.Stats().Active == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prompt did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// Reading configuration must use the existing snapshot, not prepare or reserve.
	for _, tc := range []struct {
		subject, chatID string
		want            int
	}{{"alice", "prepared-first", 200}, {"bob", "prepared-first", 403}, {"", "prepared-first", 403}, {"alice", "missing-snapshot", 403}, {"alice", "../bad", 400}} {
		r := httptest.NewRequest("GET", "/api/agent-engines/sessions/config?chatId="+tc.chatID, nil)
		if tc.subject != "" {
			r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: tc.subject}))
		}
		w := httptest.NewRecorder()
		fixture.server.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("inspect %s/%s: %d %s", tc.subject, tc.chatID, w.Code, w.Body.String())
		}
		if tc.want == 200 {
			var response struct {
				Data dto.PreparedSession `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.ChatID != tc.chatID || response.Data.PreparationID != p.PreparationID || !strings.Contains(w.Body.String(), "Test model") {
				t.Fatalf("missing snapshot: %s %v", w.Body.String(), err)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("live configuration must not be cached")
			}
		}
	}
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	// An owned but unprepared Chat must stay unprepared after a read.
	if _, _, err := fixture.chats.(*chat.FileStore).EnsureChatWithSource("snapshot-only", created.Key, "", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	rGet := httptest.NewRequest("GET", "/api/agent-engines/sessions/config?chatId=snapshot-only", nil)
	rGet = rGet.WithContext(WithPrincipal(rGet.Context(), &Principal{Subject: "alice"}))
	wGet := httptest.NewRecorder()
	fixture.server.ServeHTTP(wGet, rGet)
	if wGet.Code != 409 || !strings.Contains(wGet.Body.String(), "session_configuration_stale") {
		t.Fatalf("snapshot read prepared a Chat: %d %s", wGet.Code, wGet.Body.String())
	}
	fixture.server.deps.Config.Auth.Enabled = true
	r := httptest.NewRequest("POST", "/api/agent-engines/sessions/prepare", strings.NewReader(string(encodeProjectEngine(t, map[string]any{"chatId": "missing-login", "agentKey": created.Key}))))
	w := httptest.NewRecorder()
	fixture.server.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("authenticated deployment accepted anonymous preparation: %d %s", w.Code, w.Body.String())
	}
}

func TestRemovedDesktopBridgeRegistrationIsNotRouted(t *testing.T) {
	fixture := newTestFixture(t)
	r := httptest.NewRequest("PUT", "/api/desktop/acp-bridges", strings.NewReader(`{"bridgeId":"old"}`))
	r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: "app", Claims: map[string]any{"scope": "app", "deviceId": "desktop"}}))
	w := httptest.NewRecorder()
	fixture.server.router.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("removed registration route remains: %d %s", w.Code, w.Body.String())
	}
}

func TestMain(m *testing.M) {
	if handled, code := processgroup.RunGuardian(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestStandaloneEngineManagementRejectsBrowserAndUsesPrivateAuthority(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	engines, err := agentengine.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engines.Close()
	if err = engines.Restore(); err != nil {
		t.Fatal(err)
	}
	if err = engines.PublishLocalManagement("8080"); err != nil {
		t.Fatal(err)
	}
	access, err := agentengine.ReadLocalManagement(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{AgentEngines: engines}, router: http.NewServeMux()}
	for _, tc := range []struct {
		name, remote, origin, secret string
		status                       int
	}{
		{"admin", "127.0.0.1:1234", "", access.Secret, 200},
		{"browser", "127.0.0.1:1234", "http://localhost", access.Secret, 403},
		{"remote", "192.0.2.1:1234", "", access.Secret, 403},
		{"app token", "127.0.0.1:1234", "", "ordinary-app-token", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/local/agent-plugins", nil)
			req.RemoteAddr = tc.remote
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Authorization", "ZenMindEngine "+tc.secret)
			req.Header.Set("X-ZenMind-Host", access.HostID)
			req.Header.Set("X-ZenMind-Engine-Epoch", access.Epoch)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), access.Secret) {
				t.Fatal("secret leaked")
			}
		})
	}
}
