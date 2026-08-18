package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/contracts"
)

const cancelledOutboundRequestTTL = 30 * time.Second

func (h *Hub) InvokeClientRequest(
	ctx context.Context,
	target contracts.ClientTarget,
	request contracts.ClientRequest,
	onFrame func(contracts.ClientResponseFrame) error,
) error {
	conn, ok := h.resolveClientConnection(target)
	if !ok {
		return contracts.ErrClientTargetUnavailable
	}
	payload, err := json.Marshal(request.Payload)
	if err != nil {
		return err
	}
	frames, cleanup, err := conn.OpenOutboundRequest(RequestFrame{
		Frame:   FrameRequest,
		Type:    request.Type,
		ID:      request.ID,
		Payload: payload,
	})
	if err != nil {
		if conn.isClosed() {
			return contracts.ErrClientDisconnected
		}
		return err
	}
	defer cleanup()
	for {
		select {
		case <-ctx.Done():
			conn.SendPush("desktop.bridge.cancel", map[string]any{"requestId": request.ID})
			conn.ignoreOutboundRequest(request.ID, cancelledOutboundRequestTTL)
			return ctx.Err()
		case <-conn.Done():
			return contracts.ErrClientDisconnected
		case data, open := <-frames:
			if !open {
				return contracts.ErrClientDisconnected
			}
			var frame contracts.ClientResponseFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				return fmt.Errorf("decode client response frame: %w", err)
			}
			frame.Frame = strings.TrimSpace(frame.Frame)
			frame.Type = strings.TrimSpace(frame.Type)
			frame.ID = strings.TrimSpace(frame.ID)
			if frame.ID != request.ID {
				return fmt.Errorf("client response id does not match request")
			}
			if onFrame != nil {
				if err := onFrame(frame); err != nil {
					conn.SendPush("desktop.bridge.cancel", map[string]any{"requestId": request.ID})
					conn.ignoreOutboundRequest(request.ID, cancelledOutboundRequestTTL)
					return err
				}
			}
			switch strings.ToLower(frame.Frame) {
			case FrameResponse, FrameError:
				return nil
			case FrameStream:
				if frame.Reason != "" {
					return fmt.Errorf("client stream ended without a response frame")
				}
			default:
				return fmt.Errorf("client returned unsupported frame %q", frame.Frame)
			}
		}
	}
}
