package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-platform/internal/contracts"
)

func TestRunControlHTTPRequiresAndValidatesAgentKey(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-agent-check",
		ChatID:   "chat-agent-check",
		AgentKey: "mock-agent",
	})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{
			name:   "attach missing agentKey",
			method: http.MethodGet,
			path:   "/api/attach?runId=run-agent-check",
			status: http.StatusBadRequest,
		},
		{
			name:   "attach mismatched agentKey",
			method: http.MethodGet,
			path:   "/api/attach?runId=run-agent-check&agentKey=other-agent",
			status: http.StatusForbidden,
		},
		{
			name:   "submit missing agentKey",
			method: http.MethodPost,
			path:   "/api/submit",
			body:   `{"runId":"run-agent-check","awaitingId":"await-agent-check","params":[]}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "submit mismatched agentKey",
			method: http.MethodPost,
			path:   "/api/submit",
			body:   `{"agentKey":"other-agent","runId":"run-agent-check","awaitingId":"await-agent-check","params":[]}`,
			status: http.StatusForbidden,
		},
		{
			name:   "steer missing agentKey",
			method: http.MethodPost,
			path:   "/api/steer",
			body:   `{"runId":"run-agent-check","message":"continue"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "steer mismatched agentKey",
			method: http.MethodPost,
			path:   "/api/steer",
			body:   `{"agentKey":"other-agent","runId":"run-agent-check","message":"continue"}`,
			status: http.StatusForbidden,
		},
		{
			name:   "interrupt missing agentKey",
			method: http.MethodPost,
			path:   "/api/interrupt",
			body:   `{"runId":"run-agent-check"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "interrupt mismatched agentKey",
			method: http.MethodPost,
			path:   "/api/interrupt",
			body:   `{"agentKey":"other-agent","runId":"run-agent-check"}`,
			status: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRunControlProxyMismatchReturnsForbiddenWithoutForwarding(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-proxy-agent-check",
		ChatID:   "chat-proxy-agent-check",
		AgentKey: "proxy-agent",
	})
	route := &proxyRunRoute{
		runID:    "run-proxy-agent-check",
		chatID:   "chat-proxy-agent-check",
		agentKey: "proxy-agent",
		send:     make(chan map[string]any, 1),
		done:     make(chan struct{}),
	}
	fixture.server.registerProxyRun(route)
	defer fixture.server.unregisterProxyRun(route.runID, route)

	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "submit",
			path: "/api/submit",
			body: `{"agentKey":"other-agent","runId":"run-proxy-agent-check","awaitingId":"await-proxy-agent-check","params":[]}`,
		},
		{
			name: "interrupt",
			path: "/api/interrupt",
			body: `{"agentKey":"other-agent","runId":"run-proxy-agent-check"}`,
		},
		{
			name: "steer",
			path: "/api/steer",
			body: `{"agentKey":"other-agent","runId":"run-proxy-agent-check","message":"continue"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			select {
			case msg := <-route.send:
				t.Fatalf("did not expect proxy forward on mismatch, got %#v", msg)
			case <-time.After(10 * time.Millisecond):
			}
		})
	}
}

func TestRunControlProxyForwardsSubmitInterruptAndSteer(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-proxy-forward",
		ChatID:   "chat-proxy-forward",
		AgentKey: "proxy-agent",
	})
	route := &proxyRunRoute{
		runID:    "run-proxy-forward",
		chatID:   "chat-proxy-forward",
		agentKey: "proxy-agent",
		send:     make(chan map[string]any, 3),
		done:     make(chan struct{}),
	}
	fixture.server.registerProxyRun(route)
	defer fixture.server.unregisterProxyRun(route.runID, route)

	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{
			name: "submit",
			path: "/api/submit",
			body: `{"agentKey":"proxy-agent","runId":"run-proxy-forward","awaitingId":"await-proxy-forward","params":[]}`,
			want: "request.submit",
		},
		{
			name: "interrupt",
			path: "/api/interrupt",
			body: `{"agentKey":"proxy-agent","runId":"run-proxy-forward"}`,
			want: "request.interrupt",
		},
		{
			name: "steer",
			path: "/api/steer",
			body: `{"agentKey":"proxy-agent","runId":"run-proxy-forward","message":"continue"}`,
			want: "request.steer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			select {
			case msg := <-route.send:
				if msg["type"] != tc.want {
					t.Fatalf("expected %s, got %#v", tc.want, msg)
				}
				if tc.name == "steer" {
					payload, _ := msg["payload"].(map[string]any)
					if payload == nil || payload["steerId"] == "" {
						t.Fatalf("expected generated steerId in %#v", msg)
					}
				}
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for proxy forward")
			}
		})
	}
}
