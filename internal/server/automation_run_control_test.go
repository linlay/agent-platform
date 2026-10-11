package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
	platformws "agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestInProcessRunControlOnlyAcceptsEmptyOwnerAndAuthenticatedDesktopMain(t *testing.T) {
	fixture := newTestFixture(t)
	main := controlscope.Scope{Transport: "ws", Lane: "main", Subject: "user", Boundary: "subject:user\x00device:device"}
	for _, test := range []struct {
		name          string
		owner, caller controlscope.Scope
		authenticated bool
		allowed       bool
	}{
		{name: "empty owner", caller: main, authenticated: true, allowed: true},
		{name: "untrusted main", caller: main},
		{name: "empty caller"},
		{name: "empty caller with main flag", authenticated: true},
		{name: "no subject", caller: controlscope.Scope{Transport: "ws", Lane: "main", Boundary: "device:device"}, authenticated: true},
		{name: "no boundary", caller: controlscope.Scope{Transport: "ws", Lane: "main", Subject: "user"}, authenticated: true},
		{name: "http", caller: controlscope.Scope{Transport: "http", Subject: "user"}, authenticated: true},
		{name: "btw", caller: controlscope.Scope{Transport: "ws", Lane: "btw", Subject: "user", Boundary: main.Boundary}, authenticated: true},
		{name: "explain", caller: controlscope.Scope{Transport: "ws", Lane: "explain", Subject: "user", Boundary: main.Boundary}, authenticated: true},
		{name: "legacy internal", owner: controlscope.Scope{Transport: "internal"}, caller: main, authenticated: true},
		{name: "partial subject", owner: controlscope.Scope{Subject: "user"}, caller: main, authenticated: true},
		{name: "partial boundary", owner: controlscope.Scope{Boundary: main.Boundary}, caller: main, authenticated: true},
		{name: "partial lane", owner: controlscope.Scope{Lane: "main"}, caller: main, authenticated: true},
		{name: "http owner", owner: controlscope.Scope{Transport: "http", Subject: "user"}, caller: main, authenticated: true},
		{name: "another ws device", owner: controlscope.Scope{Transport: "ws", Lane: "main", Subject: "user", Boundary: "another-device"}, caller: main, authenticated: true},
		{name: "another ws lane", owner: controlscope.Scope{Transport: "ws", Lane: "btw", Subject: "user", Boundary: main.Boundary}, caller: main, authenticated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := fixture.server.runControlScopes().Bind(test.name, test.owner); err != nil {
				t.Fatal(err)
			}
			failure := fixture.server.validateRunControl(test.name, test.caller, test.authenticated)
			if (failure == nil) != test.allowed {
				t.Fatalf("control authorization: %v, allowed=%v", failure, test.allowed)
			}
			if failure != nil && failure.Status != http.StatusForbidden {
				t.Fatalf("want 403, got %#v", failure)
			}
			stored, err := fixture.server.runControlScopes().Load(test.name)
			if err != nil || stored != test.owner {
				t.Fatalf("control changed owner: %#v %v", stored, err)
			}
		})
	}
	if err := fixture.server.validateRunControl("missing", main, true); err == nil || err.Code != "run_control_identity_unavailable" {
		t.Fatalf("missing identity authorized: %v", err)
	}
}

func TestRunningAutomationAllowsAllDesktopMainControls(t *testing.T) {
	var privateKey *rsa.PrivateKey
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"running"}}]}`)
		<-r.Context().Done()
	}, testFixtureOptions{
		notifications: platformws.NewHub(),
		setupRuntime: func(root string, cfg *config.Config) {
			var publicKeyPath string
			privateKey, publicKeyPath = writeTestJWTKeyPair(t, root)
			cfg.Auth = config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicKeyPath, Issuer: "automation-control-test"}
			cfg.WebSocket.HeartbeatInterval = 30
		},
	})
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := make(chan chat.RunStart, 1)
	done := make(chan error, 1)
	dispatcher := automation.NewDispatcher(func(ctx context.Context, req api.QueryRequest, hooks automation.QueryRunHooks) (automation.QueryRunResult, error) {
		result, err := fixture.server.deps.Runtime.ExecuteQueryWithHooks(ctx, queryCommandFromAPI(req), runtimetypes.QueryHooks{OnRunStarted: func(start chat.RunStart) {
			starts <- start
			if hooks.OnRunStarted != nil {
				hooks.OnRunStarted(start)
			}
		}})
		return automation.QueryRunResult{Completion: result.Completion, ErrorMessage: result.ErrorMessage}, err
	}, nil, nil)
	go func() {
		done <- dispatcher.Dispatch(ctx, automation.Definition{ID: "desktop-control", Enabled: true, AgentKey: "mock-agent", Query: automation.Query{Message: "keep running"}}, "Asia/Shanghai")
	}()
	var start chat.RunStart
	select {
	case start = <-starts:
	case err := <-done:
		t.Fatalf("Automation ended before starting: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Automation did not start")
	}
	owner, err := fixture.server.runControlScopes().Load(start.RunID)
	if err != nil || owner != (controlscope.Scope{}) {
		t.Fatalf("Automation owner: %#v %v", owner, err)
	}
	ownerFiles, err := filepath.Glob(filepath.Join(fixture.server.runControlScopes().Root, "*.json"))
	if err != nil || len(ownerFiles) != 1 {
		t.Fatalf("owner files: %v %v", ownerFiles, err)
	}
	before, err := os.ReadFile(ownerFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	signToken := func(expiresAt int64) string {
		return mustSignRS256JWT(t, privateKey, map[string]any{
			"sub": "desktop-user", "scope": "app", "deviceId": "desktop-device", "iss": "automation-control-test", "exp": expiresAt,
		})
	}
	token := signToken(4102444800)
	dial := func(source, bearer string) *gws.Conn {
		t.Helper()
		conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+
			"/ws?source="+source+"&deviceId=desktop-device&token="+url.QueryEscape(bearer), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		readConnectedPush(t, conn)
		return conn
	}
	readID := func(conn *gws.Conn, id string) selectionLaneFrame {
		t.Helper()
		for {
			frame := readSelectionLaneFrame(t, conn)
			if frame.ID == id {
				return frame
			}
		}
	}
	payload := map[string]any{"runId": start.RunID, "agentKey": "mock-agent", "message": "continue", "accessLevel": "full_access", "lastSeq": 0}
	routes := []string{"/api/attach", "/api/detach", "/api/steer", "/api/interrupt", "/api/access-level"}
	for _, source := range []string{"webclient", "desktop-btw", "desktop-explain"} {
		conn := dial(source, token)
		for i, route := range routes {
			id := fmt.Sprintf("reject-%s-%d", source, i)
			sendSelectionLaneRequest(t, conn, id, route, payload)
			if frame := readID(conn, id); frame.Frame != platformws.FrameError || frame.Code != 403 || frame.Type != "run_transport_mismatch" {
				t.Fatalf("%s %s authorized: %#v", source, route, frame)
			}
		}
	}
	for _, route := range routes {
		if route == "/api/detach" {
			continue // There is no public HTTP detach route.
		}
		method, path := http.MethodPost, route
		if route == "/api/attach" {
			method, path = http.MethodGet, route+"?runId="+start.RunID+"&agentKey=mock-agent"
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(marshalPayload(payload)))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "run_transport_mismatch") {
			t.Fatalf("HTTP %s authorized: %d %s", route, rec.Code, rec.Body.String())
		}
	}
	expiresAt := time.Now().Add(5 * time.Second).Truncate(time.Second)
	conn := dial("desktop-main", signToken(expiresAt.Unix()))
	// Keep the authenticated socket open past expiry but before its heartbeat.
	// Every control must fail immediately, then work again after a valid refresh.
	time.Sleep(time.Until(expiresAt) + 10*time.Millisecond)
	for i, route := range routes {
		id := fmt.Sprintf("reject-expired-%d", i)
		sendSelectionLaneRequest(t, conn, id, route, payload)
		if frame := readID(conn, id); frame.Frame != platformws.FrameError || frame.Code != 403 || frame.Type != "run_transport_mismatch" {
			t.Fatalf("expired Desktop Main %s authorized: %#v", route, frame)
		}
	}
	sendSelectionLaneRequest(t, conn, "refresh", "auth.refresh", map[string]any{"token": token})
	if frame := readID(conn, "refresh"); frame.Frame != platformws.FrameResponse || frame.Code != 0 {
		t.Fatalf("valid Desktop Main token refresh failed: %#v", frame)
	}
	var attachID string
	for i, route := range []string{"/api/access-level", "/api/steer", "/api/attach", "/api/detach", "/api/attach"} {
		id := fmt.Sprintf("allowed-%d", i)
		sendSelectionLaneRequest(t, conn, id, route, payload)
		frame := readID(conn, id)
		if route == "/api/attach" {
			attachID = id
			if frame.Frame != platformws.FrameStream {
				t.Fatalf("attach rejected: %#v", frame)
			}
		} else if frame.Frame != platformws.FrameResponse || frame.Code != 0 || !frame.Data.Accepted {
			t.Fatalf("%s rejected: %#v", route, frame)
		}
	}
	sendSelectionLaneRequest(t, conn, "interrupt", "/api/interrupt", payload)
	accepted, terminal := false, false
	for !accepted || !terminal {
		frame := readSelectionLaneFrame(t, conn)
		if frame.Frame == platformws.FrameError {
			t.Fatalf("interrupt failed: %#v", frame)
		}
		if frame.ID == "interrupt" {
			if frame.Frame != platformws.FrameResponse || frame.Code != 0 || !frame.Data.Accepted {
				t.Fatalf("interrupt rejected: %#v", frame)
			}
			accepted = true
		}
		if frame.ID == attachID && frame.Reason != "" {
			terminal = true
		}
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Automation did not finish after interrupt")
	}
	after, err := os.ReadFile(ownerFiles[0])
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Desktop control rewrote the persisted identity: %q %v", after, err)
	}
}
