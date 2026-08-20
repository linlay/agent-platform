package ws

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestConnWebClientTargetDoesNotDependOnSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "webclient", source: "WebClient"},
		{name: "desktop chat", source: "desktop-chat"},
		{name: "desktop copilot", source: "desktop-copilot"},
		{name: "other client", source: "other-client"},
		{name: "empty", source: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{
				Context:  context.Background(),
				Subject:  "user-1",
				DeviceID: "device-1",
			})
			conn.SetClientMetadata(test.source, "device-1")
			conn.SetClientSurfaceID("surface-1")

			target := conn.WebClientTarget()
			if target.SessionID != conn.SessionID() ||
				target.BoundaryKey != "subject:user-1\x00device:device-1" ||
				target.Subject != "user-1" ||
				target.SurfaceID != "surface-1" {
				t.Fatalf("source %q changed webclient target: %#v", test.source, target)
			}
		})
	}
}
