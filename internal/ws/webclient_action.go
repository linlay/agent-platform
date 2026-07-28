package ws

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/contracts"
)

const webClientSurfaceIDMaxRunes = 128

func NormalizeWebClientDeviceID(deviceID string) string {
	return monitorNormalizeDeviceID(deviceID)
}

func NormalizeWebClientSurfaceID(surfaceID string) string {
	surfaceID = strings.TrimSpace(surfaceID)
	if utf8.RuneCountInString(surfaceID) <= webClientSurfaceIDMaxRunes {
		return surfaceID
	}
	return string([]rune(surfaceID)[:webClientSurfaceIDMaxRunes])
}

func (c *Conn) SetClientSurfaceID(surfaceID string) {
	if c == nil {
		return
	}
	c.clientInfoMu.Lock()
	c.surfaceID = NormalizeWebClientSurfaceID(surfaceID)
	c.clientInfoMu.Unlock()
}

func (c *Conn) WebClientTarget() contracts.WebClientTarget {
	if c == nil {
		return contracts.WebClientTarget{}
	}
	c.clientInfoMu.RLock()
	surfaceID := c.surfaceID
	c.clientInfoMu.RUnlock()
	c.authMu.RLock()
	subject := strings.TrimSpace(c.auth.Subject)
	c.authMu.RUnlock()
	return contracts.WebClientTarget{
		SessionID:   c.SessionID(),
		BoundaryKey: c.ClientBoundaryKey(),
		Subject:     subject,
		SurfaceID:   NormalizeWebClientSurfaceID(surfaceID),
	}
}

func webClientConnectionKey(boundaryKey string, surfaceID string) string {
	boundaryKey = strings.TrimSpace(boundaryKey)
	surfaceID = NormalizeWebClientSurfaceID(surfaceID)
	if boundaryKey == "" || surfaceID == "" {
		return ""
	}
	return boundaryKey + "\x00surface:" + surfaceID
}

func (h *Hub) registerWebClientLocked(conn *Conn) *Conn {
	if h == nil || conn == nil {
		return nil
	}
	target := conn.WebClientTarget()
	key := webClientConnectionKey(target.BoundaryKey, target.SurfaceID)
	if key == "" {
		return nil
	}
	replaced := h.webClientConns[key]
	if replaced == conn {
		return nil
	}
	if replaced != nil {
		delete(h.webClientKeys, replaced)
	}
	h.webClientKeys[conn] = key
	h.webClientConns[key] = conn
	return replaced
}

func (h *Hub) unregisterWebClientLocked(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	key, ok := h.webClientKeys[conn]
	if !ok {
		return
	}
	delete(h.webClientKeys, conn)
	if h.webClientConns[key] != conn {
		return
	}
	delete(h.webClientConns, key)
}

func (h *Hub) resolveWebClientConnection(target contracts.WebClientTarget) (*Conn, bool) {
	if h == nil || target.IsZero() {
		return nil, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if key := webClientConnectionKey(target.BoundaryKey, target.SurfaceID); key != "" {
		if conn := h.webClientConns[key]; conn != nil && !conn.isClosed() {
			return conn, true
		}
	}
	if strings.TrimSpace(target.SessionID) != "" {
		for conn := range h.conns {
			if conn != nil && !conn.isClosed() && conn.SessionID() == strings.TrimSpace(target.SessionID) {
				return conn, true
			}
		}
	}
	return nil, false
}

func (h *Hub) InvokeWebClientAction(
	ctx context.Context,
	target contracts.WebClientTarget,
	request contracts.WebClientActionRequest,
) (contracts.WebClientActionResponse, error) {
	conn, ok := h.resolveWebClientConnection(target)
	if !ok {
		return contracts.WebClientActionResponse{}, contracts.ErrWebClientTargetUnavailable
	}
	payload, err := json.Marshal(request.Payload)
	if err != nil {
		return contracts.WebClientActionResponse{}, err
	}
	frames, cleanup, err := conn.OpenOutboundRequest(RequestFrame{
		Frame:   FrameRequest,
		Type:    request.Type,
		ID:      request.ID,
		Payload: payload,
	})
	if err != nil {
		if conn.isClosed() {
			return contracts.WebClientActionResponse{}, contracts.ErrWebClientDisconnected
		}
		return contracts.WebClientActionResponse{}, err
	}
	defer cleanup()
	select {
	case <-ctx.Done():
		return contracts.WebClientActionResponse{}, ctx.Err()
	case <-conn.Done():
		return contracts.WebClientActionResponse{}, contracts.ErrWebClientDisconnected
	case data, open := <-frames:
		if !open {
			return contracts.WebClientActionResponse{}, contracts.ErrWebClientDisconnected
		}
		var response contracts.WebClientActionResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return contracts.WebClientActionResponse{}, err
		}
		return response, nil
	}
}
