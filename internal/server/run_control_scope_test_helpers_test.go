package server

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/controlscope"
	"context"
	"encoding/json"
	gws "github.com/gorilla/websocket"
	"strings"
	"testing"
	"time"
)

// Synthetic fixtures bypass query admission, so explicitly seed its durable identity.
func bindTestRunControl(t testing.TB, s *Server, runID, transport, boundary string) {
	t.Helper()
	if s.deps.Config.Paths.StateDir == "" && s.deps.Config.Paths.ChatsDir == "" {
		s.deps.Config.Paths.StateDir = t.TempDir()
	}
	if err := s.runControlScopes().Bind(runID, controlscope.Scope{Transport: transport, Lane: "main", Boundary: boundary}); err != nil {
		t.Fatal(err)
	}
}
func registerHTTPTestRun(t testing.TB, fixture testFixture, ctx context.Context, session contracts.QuerySession) (context.Context, *contracts.RunControl, contracts.ActiveRun) {
	t.Helper()
	bindTestRunControl(t, fixture.server, session.RunID, "http", "")
	return fixture.runs.Register(ctx, session)
}
func registerWSTestRun(t testing.TB, fixture testFixture, ctx context.Context, session contracts.QuerySession) (context.Context, *contracts.RunControl, contracts.ActiveRun) {
	t.Helper()
	bindTestRunControl(t, fixture.server, session.RunID, "ws", "")
	return fixture.runs.Register(ctx, session)
}

func wsTestControlResponse(t testing.TB, serverURL, route string, payload any) []byte {
	t.Helper()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(serverURL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]any{"frame": "request", "type": route, "id": "control", "payload": payload}); err != nil {
		t.Fatal(err)
	}
	for {
		var frame map[string]json.RawMessage
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if string(frame["id"]) == `"control"` {
			raw, _ := json.Marshal(frame)
			return raw
		}
	}
}
