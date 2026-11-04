package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func (s *Server) handleDesktopACPBridges(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// /api/desktop/* forces JWT verification even when general auth is disabled.
	// Plugin identity is asserted by the authenticated Desktop host, never by a
	// browser scope or an ACP plugin's direct unauthenticated HTTP request.
	p := PrincipalFromContext(r.Context())
	if p == nil || strings.TrimSpace(p.Subject) == "" || stringClaim(p.Claims, "scope") != "app" || firstStringClaim(p.Claims, "deviceId", "device_id") == "" {
		writeRequestError(w, &requestError{Code: "acp_registration_denied", Status: 403})
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeRequestError(w, &requestError{Code: "method_not_allowed", Status: 405})
		return
	}
	var input config.ACPRegistration
	if !decodeBoundedRequest(w, r, &input) {
		return
	}
	if s.acpRegistrations == nil {
		writeRequestError(w, &requestError{Code: "acp_registration_unavailable", Status: 503})
		return
	}
	result, err := s.acpRegistrations.Mutate(input, r.Method == http.MethodDelete)
	if err != nil {
		code, status := "acp_registration_failed", 500
		switch {
		case errors.Is(err, config.ErrACPArguments):
			code, status = "invalid_acp_registration", 400
		case errors.Is(err, config.ErrACPConflict):
			code, status = "acp_registration_conflict", 409
		case errors.Is(err, config.ErrACPConfig):
			code, status = "acp_configuration_invalid", 409
		}
		// Do not echo config contents, paths, URLs or credentials in errors.
		writeRequestError(w, &requestError{Code: code, Status: status})
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}
