package server

import (
	"context"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
)

func TestInterruptRequestHelpersStoreHTTPAndWSCauses(t *testing.T) {
	tests := []struct {
		name       string
		request    api.InterruptRequest
		wantSource string
		wantReason string
	}{
		{
			name: "http api",
			request: httpAPIUserInterruptRequest(api.InterruptRequest{
				RequestID: "request_http",
				RunID:     "run_http",
				ChatID:    "chat_http",
			}),
			wantSource: contracts.InterruptSourceHTTPAPI,
			wantReason: contracts.InterruptReasonUserCancelled,
		},
		{
			name: "ws api",
			request: wsAPIUserInterruptRequest(api.InterruptRequest{
				RequestID: "request_ws",
				RunID:     "run_ws",
				ChatID:    "chat_ws",
			}),
			wantSource: contracts.InterruptSourceWSAPI,
			wantReason: contracts.InterruptReasonUserCancelled,
		},
		{
			name: "proxy ws",
			request: wsAPIUserInterruptRequest(proxyWSInterruptRequest(api.InterruptRequest{
				RequestID: "request_proxy",
				RunID:     "run_proxy",
				ChatID:    "chat_proxy",
			})),
			wantSource: contracts.InterruptSourceProxyWS,
			wantReason: contracts.InterruptReasonProxyInterrupt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := runstate.NewManager()
			_, control, _ := manager.Register(context.Background(), contracts.QuerySession{
				RunID:  tt.request.RunID,
				ChatID: tt.request.ChatID,
			})
			ack := manager.Interrupt(tt.request)
			if !ack.Accepted {
				t.Fatalf("expected interrupt accepted, got %#v", ack)
			}
			info, ok := control.InterruptInfo()
			if !ok {
				t.Fatalf("expected interrupt info")
			}
			if info.Source != tt.wantSource || info.Reason != tt.wantReason {
				t.Fatalf("unexpected interrupt info: %#v", info)
			}
			if info.RequestID != tt.request.RequestID || info.ChatID != tt.request.ChatID {
				t.Fatalf("unexpected interrupt metadata: %#v", info)
			}
		})
	}
}
