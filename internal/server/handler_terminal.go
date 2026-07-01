package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	terminalpkg "agent-platform/internal/terminal"
)

const terminalDeviceIDMaxRunes = 128

type terminalSessionsResponse struct {
	Sessions []terminalpkg.SessionInfo `json:"sessions"`
}

func (s *Server) handleTerminalSessions(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.terminals == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "terminal manager is not configured"))
		return
	}
	ownerKey := terminalOwnerKeyFromRequest(r)
	if ownerKey == "" {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "terminal owner is required"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(terminalSessionsResponse{
		Sessions: s.terminals.List(ownerKey),
	}))
}

func terminalOwnerKeyFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	deviceID := terminalDeviceIDFromRequest(r)
	principal := PrincipalFromContext(r.Context())
	if principal != nil {
		subject := strings.TrimSpace(principal.Subject)
		if subject != "" {
			if deviceID == "" {
				deviceID = normalizeTerminalBoundaryText(firstStringClaim(principal.Claims, "deviceId", "device_id"), terminalDeviceIDMaxRunes)
			}
			if deviceID != "" {
				return "subject:" + subject + "\x00device:" + deviceID
			}
			return ""
		}
	}
	if deviceID != "" {
		return "device:" + deviceID
	}
	return ""
}

func terminalDeviceIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	query := r.URL.Query()
	deviceID := strings.TrimSpace(query.Get("deviceId"))
	if deviceID == "" {
		deviceID = strings.TrimSpace(query.Get("device_id"))
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(r.Header.Get("X-Agent-Webclient-Device-Id"))
	}
	return normalizeTerminalBoundaryText(deviceID, terminalDeviceIDMaxRunes)
}

func normalizeTerminalBoundaryText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
