package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
)

const (
	webClientDeviceIDHeader  = "X-Agent-WebClient-Device-Id"
	webClientSurfaceIDHeader = "X-Agent-WebClient-Surface-Id"
)

func webClientTargetFromHTTPRequest(r *http.Request) contracts.WebClientTarget {
	if r == nil {
		return contracts.WebClientTarget{}
	}
	surfaceID := ws.NormalizeWebClientSurfaceID(r.Header.Get(webClientSurfaceIDHeader))
	if surfaceID == "" {
		return contracts.WebClientTarget{}
	}
	principal := PrincipalFromContext(r.Context())
	subject := ""
	deviceID := ""
	if principal != nil {
		subject = strings.TrimSpace(principal.Subject)
		deviceID = firstStringClaim(principal.Claims, "deviceId", "device_id")
	}
	if deviceID == "" {
		deviceID = ws.NormalizeWebClientDeviceID(r.Header.Get(webClientDeviceIDHeader))
	}
	boundaryKey := ws.ClientBoundaryKeyForIdentity(subject, deviceID)
	if boundaryKey == "" {
		return contracts.WebClientTarget{}
	}
	return contracts.WebClientTarget{
		BoundaryKey: boundaryKey,
		Subject:     subject,
		SurfaceID:   surfaceID,
	}
}
