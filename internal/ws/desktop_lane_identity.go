package ws

import "strings"

func isReservedDesktopLane(value string) bool {
	switch value {
	case desktopMainClientSource, desktopBTWClientSource, desktopSelectionExplainClientSource:
		return true
	default:
		return false
	}
}

func desktopLaneDeviceAuthorized(auth AuthSession, deviceID string) bool {
	authDeviceID := monitorNormalizeDeviceID(auth.DeviceID)
	return authDeviceID != "" && deviceID == authDeviceID &&
		(auth.AuthDisabled || (auth.DeviceIDVerified && strings.TrimSpace(auth.Scope) == "app"))
}

// Physical Desktop identities must be authorized before the generic surface map
// can reserve or replace their keys. An absent surfaceId remains a session-only
// target; an explicit physical surfaceId must name that same lane.
func desktopLaneMetadataAuthorized(source, deviceID, surfaceID string, auth AuthSession) bool {
	source = monitorNormalizeSource(source)
	surfaceID = NormalizeWebClientSurfaceID(surfaceID)
	if !isReservedDesktopLane(source) && !isReservedDesktopLane(surfaceID) {
		return true
	}
	if !isReservedDesktopLane(source) || (surfaceID != "" && surfaceID != source) {
		return false
	}
	return desktopLaneDeviceAuthorized(auth, monitorNormalizeDeviceID(deviceID))
}

func (c *Conn) canRegisterClientIdentity() bool {
	if c == nil {
		return false
	}
	source, deviceID := c.monitorClientMetadata()
	c.clientInfoMu.RLock()
	surfaceID := c.surfaceID
	c.clientInfoMu.RUnlock()
	c.authMu.RLock()
	auth := c.auth
	c.authMu.RUnlock()
	return desktopLaneMetadataAuthorized(source, deviceID, surfaceID, auth)
}
