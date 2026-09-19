package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/ws"
)

func TestRunQueryInheritsParentConnectionWithoutCancellation(t *testing.T) {
	for _, scope := range []controlscope.Scope{
		{Transport: "ws", Lane: "main", Subject: "app", Boundary: "desktop"},
		{Transport: "ws", Lane: "btw", Subject: "app", Boundary: "desktop"},
		{Transport: "http", Lane: "main", Subject: "app"},
	} {
		t.Run(scope.Transport+"/"+scope.Lane, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{notifications: ws.NewHub()})
			if err := fixture.server.runControlScopes().Bind("parent", scope); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			started, err := fixture.server.StartRun(ctx, contracts.RunStartRequest{
				AgentKey: "mock-agent", Message: "independent task",
				Origin: contracts.RunOrigin{AgentKey: "zenmi", RunID: "parent", ToolID: "call"},
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := fixture.server.runControlScopes().Load(started.RunID)
			if err != nil || got != scope {
				t.Fatalf("scope=%#v want=%#v err=%v", got, scope, err)
			}
			done := waitRunTerminal(t, fixture.server, started.RunID)
			if done.Status != "completed" {
				t.Fatalf("parent cancellation propagated: %#v", done)
			}
		})
	}
}

func TestHITLSubmitAcrossCreationScopes(t *testing.T) {
	for _, origin := range []controlscope.Scope{
		{Transport: "ws", Lane: "explain", Subject: "app", Boundary: "desktop-device"},
		{Transport: "http", Lane: "main", Subject: "app"},
		{},
	} {
		for _, transport := range []string{"http", "ws"} {
			t.Run(origin.Transport+"-to-"+transport, func(t *testing.T) {
				fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
					writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
				}, testFixtureOptions{notifications: ws.NewHub()})
				const runID = "cross-submit"
				if err := fixture.server.runControlScopes().Bind(runID, origin); err != nil {
					t.Fatal(err)
				}
				_, control, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
					RunID: runID, ChatID: "cross-chat", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", ""),
				})
				control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "approval", Mode: "approval", ItemCount: 1})
				server := httptest.NewServer(fixture.server)
				defer server.Close()
				submit := func(agent, awaiting, decision string) (int, string, bool) {
					t.Helper()
					payload := map[string]any{"runId": runID, "agentKey": agent, "awaitingId": awaiting, "params": []any{map[string]any{"id": "tool", "decision": decision}}}
					var raw []byte
					if transport == "ws" {
						raw = wsTestControlResponse(t, server.URL, "/api/submit", payload)
					} else {
						body, _ := json.Marshal(payload)
						rec := httptest.NewRecorder()
						req := httptest.NewRequest(http.MethodPost, "/api/submit", strings.NewReader(string(body)))
						req.Header.Set("X-Agent-WebClient-Device-Id", "phone-device")
						fixture.server.ServeHTTP(rec, req)
						raw = rec.Body.Bytes()
					}
					var response struct {
						Code int `json:"code"`
						Data struct {
							Accepted bool `json:"accepted"`
						} `json:"data"`
					}
					if err := json.Unmarshal(raw, &response); err != nil {
						t.Fatalf("decode: %s: %v", raw, err)
					}
					return response.Code, string(raw), response.Data.Accepted
				}
				for _, invalid := range []struct{ agent, awaiting, decision string }{
					{"other-agent", "approval", "approve"},
					{"mock-agent", "wrong-awaiting", "approve"},
					{"mock-agent", "approval", "invalid"},
				} {
					_, raw, accepted := submit(invalid.agent, invalid.awaiting, invalid.decision)
					if accepted {
						t.Fatalf("invalid accepted: %s", raw)
					}
					if strings.Contains(raw, "run_transport_mismatch") || strings.Contains(raw, "run_control_identity_mismatch") || strings.Contains(raw, "run_lane_mismatch") {
						t.Fatalf("blocked before approval validation: %s", raw)
					}
				}
				code, raw, accepted := submit("mock-agent", "approval", "approve")
				if code != 0 || !accepted {
					t.Fatalf("cross-device/transport submit failed: %s", raw)
				}
				_, raw, accepted = submit("mock-agent", "approval", "approve")
				if accepted {
					t.Fatalf("duplicate accepted: %s", raw)
				}
				got, err := fixture.server.runControlScopes().Load(runID)
				if err != nil || got != origin {
					t.Fatalf("submit rewrote creation scope: %#v %v", got, err)
				}
			})
		}
	}
}

func TestHITLSubmitStillRequiresAuthentication(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicKeyPath, Issuer: "agent-platform-local"}
	deps := fixture.server.deps
	deps.Config = fixture.cfg
	deps.Notifications = ws.NewHub()
	server, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/api/submit"}, {http.MethodGet, "/ws"}} {
		t.Run(route.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"agentKey":"mock-agent","runId":"run","awaitingId":"approval","params":[]}`))
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("missing authentication: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRunQueryRejectsMissingOrInvalidParentTransport(t *testing.T) {
	for _, transport := range []string{"missing", "", "internal"} {
		t.Run(transport, func(t *testing.T) {
			fixture := newTestFixture(t)
			if transport != "missing" {
				if err := fixture.server.runControlScopes().Bind("parent", controlscope.Scope{Transport: transport}); err != nil {
					t.Fatal(err)
				}
			}
			started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", Message: "must not start", Origin: contracts.RunOrigin{RunID: "parent", ToolID: "call"}})
			if err == nil || started.RunID != "" {
				t.Fatalf("created without parent transport: %#v %v", started, err)
			}
		})
	}
}
