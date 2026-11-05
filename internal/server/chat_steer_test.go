package server

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts/queryinput"
	runtimeproxy "agent-platform/internal/runtime/proxy"
)

func TestChatSteerProxyDoesNotForwardSource(t *testing.T) {
	fixture := newTestFixture(t)
	for _, transport := range []string{"ws", "sse"} {
		t.Run(transport, func(t *testing.T) {
			route := runtimeproxy.NewRoute("target-"+transport, "target-chat", "target-agent")
			route.Transport = transport
			var received map[string]any
			if transport == "sse" {
				upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accepted": true, "status": "accepted", "runId": route.RunID, "steerId": "id"}})
				}))
				defer upstream.Close()
				route.BaseURL = upstream.URL
			}
			fixture.server.proxyRuntime.Register(route)
			defer fixture.server.proxyRuntime.Unregister(route.RunID, route)
			source := &queryinput.Origin{Type: "agent", AgentKey: "caller", ChatID: "caller-chat", RunID: "caller-run", ToolID: "call"}
			result, statusErr, forwarded := fixture.server.forwardProxySteer(api.SteerRequest{RunID: route.RunID, AgentKey: route.AgentKey, SteerID: "id", Message: "goal", Origin: source})
			if statusErr != nil || !forwarded || !result.Accepted {
				t.Fatalf("%#v %v %v", result, statusErr, forwarded)
			}
			if transport == "ws" {
				received = (<-route.SendQueue)["payload"].(map[string]any)
			}
			if received["origin"] != nil || received["steerId"] != "id" || received["message"] != "goal" {
				t.Fatalf("upstream payload: %#v", received)
			}
		})
	}
}

func TestChatSteerProxyDistinguishesNotSentFromUnknown(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name, transport, code string
		status                int
		body                  string
		unknown               bool
	}{
		{name: "ws_closed", transport: "ws", code: "run_steer_not_sent"},
		{name: "ws_full", transport: "ws", code: "run_steer_not_sent"},
		{name: "agent_mismatch", transport: "ws"},
		{name: "no_endpoint", transport: "sse", code: "proxy_unavailable"},
		{name: "invalid_url", transport: "sse", code: "proxy_unavailable"},
		{name: "http_rejection", transport: "sse", code: "proxy_control_failed", status: 403, body: `{"code":403,"msg":"denied"}`},
		{name: "http_rejection_plaintext", transport: "sse", code: "proxy_control_failed", status: 403, body: "denied"},
		{name: "application_rejection", transport: "sse", code: "proxy_control_failed", status: 200, body: `{"code":1,"msg":"rejected"}`},
		{name: "not_accepted", transport: "sse", status: 200, body: `{"code":0,"data":{"accepted":false,"status":"unmatched"}}`},
		{name: "invalid_response", transport: "sse", code: "proxy_invalid_response", status: 200, body: "invalid", unknown: true},
		{name: "partial_rejection", transport: "sse", code: "proxy_invalid_response", status: 200, body: `{"code":1,"msg":`, unknown: true},
		{name: "invalid_data", transport: "sse", code: "proxy_invalid_response", status: 200, body: `{"code":0,"data":42}`, unknown: true},
		{name: "missing_data", transport: "sse", code: "proxy_invalid_response", status: 200, body: `{"code":0}`, unknown: true},
		{name: "null_data", transport: "sse", code: "proxy_invalid_response", status: 200, body: `{"code":0,"data":null}`, unknown: true},
		{name: "missing_ack", transport: "sse", code: "proxy_invalid_response", status: 200, body: `{"code":0,"data":{}}`, unknown: true},
		{name: "timeout", transport: "sse", code: "proxy_unavailable", unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := runtimeproxy.NewRoute("target-"+tc.name, "target-chat", "target-agent")
			route.Transport = tc.transport
			var calls atomic.Int32
			switch tc.name {
			case "ws_closed":
				close(route.Done)
			case "ws_full":
				for range cap(route.SendQueue) {
					route.SendQueue <- map[string]any{"existing": true}
				}
			case "invalid_url":
				route.BaseURL = "://invalid"
			case "no_endpoint", "agent_mismatch":
			default:
				upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if tc.name == "timeout" {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer upstream.Close()
				route.BaseURL = upstream.URL
				route.Timeout = 100 * time.Millisecond
			}
			fixture.server.proxyRuntime.Register(route)
			defer fixture.server.proxyRuntime.Unregister(route.RunID, route)
			req := api.SteerRequest{RunID: route.RunID, AgentKey: route.AgentKey, SteerID: "id", Message: "goal", Origin: &queryinput.Origin{Type: "agent", AgentKey: "caller", ChatID: "caller-chat", RunID: "caller", ToolID: "call"}}
			if tc.name == "agent_mismatch" {
				req.AgentKey = "wrong"
			}
			response, statusErr, forwarded := fixture.server.forwardProxySteer(req)
			if !forwarded {
				t.Fatal("remote control fell back to native")
			}
			if tc.name == "not_accepted" {
				if statusErr != nil || response.Accepted || response.Status != "unmatched" {
					t.Fatalf("upstream refusal changed: %#v %v", response, statusErr)
				}
			} else if statusErr == nil || statusErr.Code != tc.code || statusErr.OutcomeUnknown != tc.unknown {
				t.Fatalf("error=%#v expected code=%s unknown=%v", statusErr, tc.code, tc.unknown)
			}
			if tc.name == "agent_mismatch" && statusErr.Status != http.StatusForbidden {
				t.Fatal(statusErr)
			}
			if tc.name == "ws_closed" || tc.name == "agent_mismatch" {
				if len(route.SendQueue) != 0 {
					t.Fatal("rejected steer entered sending queue")
				}
			}
			if tc.name == "ws_full" {
				for range cap(route.SendQueue) {
					if message := <-route.SendQueue; message["existing"] != true {
						t.Fatal("timed out steer entered sending queue")
					}
				}
			}
			if tc.unknown && calls.Load() != 1 {
				t.Fatal("uncertain request was not attempted exactly once")
			}
			if tc.name == "ws_closed" {
				req.Origin = nil
				response, statusErr, _ = fixture.server.forwardProxySteer(req)
				if statusErr != nil || response.Accepted || response.Status != "unmatched" {
					t.Fatal("human WS closed-route behavior changed")
				}
			}
		})
	}
}
