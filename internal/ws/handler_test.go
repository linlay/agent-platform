package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"agent-platform/internal/config"
)

type testAuthenticator struct{}

func (testAuthenticator) VerifyToken(ctx context.Context, token string) (AuthSession, error) {
	return AuthSession{Context: ctx, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil
}

type refreshAuthenticator struct {
	auth AuthSession
	err  error
}

func (a refreshAuthenticator) VerifyToken(context.Context, string) (AuthSession, error) {
	return a.auth, a.err
}

func TestExtractToken(t *testing.T) {
	t.Run("subprotocol", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Sec-WebSocket-Protocol", "bearer.token-123")
		token, protocol, err := extractToken(req)
		if err != nil {
			t.Fatalf("extract token: %v", err)
		}
		if token != "token-123" || protocol != "bearer.token-123" {
			t.Fatalf("unexpected token extraction: token=%q protocol=%q", token, protocol)
		}
	})

	t.Run("query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ws?token=query-token", nil)
		token, protocol, err := extractToken(req)
		if err != nil {
			t.Fatalf("extract token: %v", err)
		}
		if token != "query-token" || protocol != "" {
			t.Fatalf("unexpected token extraction: token=%q protocol=%q", token, protocol)
		}
	})
}

func TestWSIntegrationGated(t *testing.T) {
	if os.Getenv("RUN_SOCKET_TESTS") == "" {
		t.Skip("set RUN_SOCKET_TESTS=1 to run websocket integration tests")
	}
	handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 4, PingInterval: 30}, time.Second, NewHub(), testAuthenticator{})
	if handler == nil {
		t.Fatalf("expected handler")
	}
}

func TestAuthRefreshValidatesExpiryBeforeUpdatingConnection(t *testing.T) {
	oldExpiry := time.Now().Add(time.Hour).UnixMilli()
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{ExpiresAt: oldExpiry})
	handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 4}, time.Second, nil, refreshAuthenticator{
		auth: AuthSession{ExpiresAt: 1_700_000_000}, // Unix seconds, invalid for public expiry
	})
	handler.routeRequest(context.Background(), conn, RequestFrame{
		Type:    "auth.refresh",
		ID:      "refresh-invalid",
		Payload: MarshalPayload(map[string]any{"token": "fresh"}),
	})
	message := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := message.frame.(ErrorFrame)
	if !ok || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("expected 422 time-contract error, got %#v", message.frame)
	}
	data, _ := frame.Data.(map[string]any)
	if data["field"] != "expiresAt" || data["location"] != "ws.auth.refresh" || data["expected"] != "epoch_ms_int64" {
		t.Fatalf("expected flat time-contract diagnostics, got %#v", frame.Data)
	}
	if conn.expiresAt() != oldExpiry {
		t.Fatalf("invalid refresh changed connection auth: got %d want %d", conn.expiresAt(), oldExpiry)
	}
}

func TestAuthRefreshOmitsAbsentExpiryAndUsesEpochMilliseconds(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
		handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 4}, time.Second, nil, refreshAuthenticator{auth: AuthSession{}})
		handler.routeRequest(context.Background(), conn, RequestFrame{Type: "auth.refresh", ID: "refresh-none", Payload: MarshalPayload(map[string]any{"token": "fresh"})})
		message := mustReadQueuedMessage(t, conn.writeQueue)
		frame, ok := message.frame.(ResponseFrame)
		if !ok {
			t.Fatalf("expected response frame, got %#v", message.frame)
		}
		data, _ := frame.Data.(map[string]any)
		if _, exists := data["expiresAt"]; exists {
			t.Fatalf("expected absent expiry to be omitted, got %#v", data)
		}
	})

	t.Run("milliseconds", func(t *testing.T) {
		const expiresAt = int64(1_700_000_000_000)
		conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
		handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 4}, time.Second, nil, refreshAuthenticator{auth: AuthSession{ExpiresAt: expiresAt}})
		handler.routeRequest(context.Background(), conn, RequestFrame{Type: "auth.refresh", ID: "refresh-ms", Payload: MarshalPayload(map[string]any{"token": "fresh"})})
		message := mustReadQueuedMessage(t, conn.writeQueue)
		frame, ok := message.frame.(ResponseFrame)
		if !ok {
			t.Fatalf("expected response frame, got %#v", message.frame)
		}
		data, _ := frame.Data.(map[string]any)
		if got, ok := data["expiresAt"].(int64); !ok || got != expiresAt {
			t.Fatalf("expected epoch-milliseconds expiry, got %#v", data)
		}
	})
}
