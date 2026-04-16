package ws

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
)

type testAuthenticator struct{}

func (testAuthenticator) VerifyToken(ctx context.Context, token string) (AuthSession, error) {
	return AuthSession{Context: ctx, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil
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
	handler := NewHandler(config.WebSocketConfig{Enabled: true, WriteQueueSize: 4, PingIntervalMs: 30000}, time.Second, NewHub(), testAuthenticator{})
	if handler == nil {
		t.Fatalf("expected handler")
	}
}
