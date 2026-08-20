package ws

import (
	"strings"
	"unicode/utf8"

	"agent-platform/internal/contracts"
)

const (
	webClientSurfaceIDMaxRunes = 128
	desktopMainClientSource    = "desktop-main"
)

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
	return h.resolveClientConnection(target)
}

func (h *Hub) resolveClientConnection(target contracts.ClientTarget) (*Conn, bool) {
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

func (h *Hub) registerDesktopMainLocked(conn *Conn) *Conn {
	if h == nil || conn == nil {
		return nil
	}
	if _, ok := conn.authenticatedDesktopMainTarget(); !ok {
		return nil
	}
	replaced := h.desktopMainConn
	h.desktopMainConn = conn
	h.desktopMainSeen = true
	if replaced == conn {
		return nil
	}
	return replaced
}

func (h *Hub) unregisterDesktopMainLocked(conn *Conn) {
	if h == nil || conn == nil || h.desktopMainConn != conn {
		return
	}
	h.desktopMainConn = nil
}

func (h *Hub) ResolveDesktopMainTarget() (contracts.ClientTarget, contracts.DesktopMainTargetState) {
	if h == nil {
		return contracts.ClientTarget{}, contracts.DesktopMainTargetMissing
	}
	h.mu.RLock()
	conn := h.desktopMainConn
	seen := h.desktopMainSeen
	h.mu.RUnlock()
	if conn == nil || conn.isClosed() {
		if seen {
			return contracts.ClientTarget{}, contracts.DesktopMainTargetDisconnected
		}
		return contracts.ClientTarget{}, contracts.DesktopMainTargetMissing
	}
	target, ok := conn.authenticatedDesktopMainTarget()
	if !ok {
		return contracts.ClientTarget{}, contracts.DesktopMainTargetDisconnected
	}
	return target, contracts.DesktopMainTargetReady
}

func (c *Conn) authenticatedDesktopMainTarget() (contracts.ClientTarget, bool) {
	if c == nil {
		return contracts.ClientTarget{}, false
	}
	source, deviceID := c.monitorClientMetadata()
	if source != desktopMainClientSource || strings.TrimSpace(deviceID) == "" {
		return contracts.ClientTarget{}, false
	}
	c.authMu.RLock()
	authDeviceID := monitorNormalizeDeviceID(c.auth.DeviceID)
	deviceIDVerified := c.auth.DeviceIDVerified
	authScope := strings.TrimSpace(c.auth.Scope)
	c.authMu.RUnlock()
	if !deviceIDVerified || authScope != "app" || authDeviceID == "" || deviceID != authDeviceID {
		return contracts.ClientTarget{}, false
	}
	target := c.WebClientTarget()
	return target, !target.IsZero()
}
