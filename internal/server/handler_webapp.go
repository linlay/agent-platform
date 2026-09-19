package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/webapp"
)

// These endpoints require a verified Desktop personal app principal even when
// the deployment permits anonymous ordinary APIs. Payload cannot choose owner.
func desktopPersonalSubject(r *http.Request) (string, error) {
	p := PrincipalFromContext(r.Context())
	if p == nil || !strings.HasPrefix(p.Subject, "desktop-user:") || stringClaim(p.Claims, "scope") != "app" || firstStringClaim(p.Claims, "deviceId", "device_id") == "" {
		return "", webapp.ErrDenied
	}
	if len(p.Subject) != 77 {
		return "", webapp.ErrDenied
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(p.Subject, "desktop-user:")); err != nil {
		return "", webapp.ErrDenied
	}
	return p.Subject, nil
}
func (s *Server) handleWebappGrant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	subject, err := desktopPersonalSubject(r)
	if err != nil {
		writeWebappError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		err = s.webappGrants.Revoke(subject, r.URL.Query().Get("grantId"))
		if err != nil {
			writeWebappError(w, err)
			return
		}
		writeJSON(w, 200, api.Success(map[string]bool{"revoked": true}))
		return
	}
	if r.Method != http.MethodPost {
		writeWebappError(w, &connectorops.Error{Code: "method_not_allowed", Status: 405})
		return
	}
	var req struct {
		AppID      string              `json:"appId"`
		Operations map[string][]string `json:"operations"`
		ChatIDs    []string            `json:"chatIds,omitempty"`
	}
	if !decodeWebappRequest(w, r, &req) {
		return
	}
	for _, id := range req.ChatIDs {
		if s.deps.Chats == nil {
			writeWebappError(w, webapp.ErrDenied)
			return
		}
		summary, e := s.deps.Chats.Summary(id)
		if e != nil || summary == nil || !queryPrincipalCanReferenceChat(r.Context(), *summary) {
			writeWebappError(w, webapp.ErrDenied)
			return
		}
	}
	grant, err := s.webappGrants.IssueWithChats(subject, req.AppID, req.Operations, req.ChatIDs)
	if err != nil {
		writeWebappError(w, err)
		return
	}
	writeJSON(w, 200, api.Success(grant))
}
func (s *Server) handleDesktopConnectorAuth(w http.ResponseWriter, r *http.Request) {
	if _, err := desktopPersonalSubject(r); err != nil {
		writeWebappError(w, err)
		return
	}
	manager := s.connectorAuth
	// Reuse the existing trusted-host auth transport, never expose it through a
	// WebApp grant. Manager owns deduplication, credential fences and expiry.

	if strings.HasSuffix(r.URL.Path, "/cancel") {
		if r.Method != http.MethodPost || r.URL.Query().Get("sessionId") == "" {
			writeWebappError(w, &connectorops.Error{Code: "invalid_arguments", Status: 400})
			return
		}
		err := manager.CancelSession(r.URL.Query().Get("id"), r.URL.Query().Get("sessionId"))
		if err != nil {
			writeWebappError(w, &connectorops.Error{Code: "auth_session_unavailable", Status: 409})
			return
		}
		writeJSON(w, 200, api.Success(map[string]any{"status": "canceled"}))
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("sessionId") != "" {
		result, err := manager.SessionStatus(r.URL.Query().Get("id"), r.URL.Query().Get("sessionId"))
		if err != nil {
			writeWebappError(w, &connectorops.Error{Code: "auth_session_unavailable", Status: 409})
			return
		}
		writeJSON(w, 200, api.Success(result))
		return
	}
	s.handleConnectorAuthWithManager(w, r, manager)
}
func (s *Server) handleWebappConnector(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeWebappError(w, &connectorops.Error{Code: "method_not_allowed", Status: 405})
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer wap_") {
		writeWebappError(w, webapp.ErrDenied)
		return
	}
	scope, grantCtx, err := s.webappGrants.Scope(strings.TrimPrefix(authorization, "Bearer "))
	if err != nil {
		writeWebappError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(grantCtx, cancel)
	defer stop()
	service := connectorops.Service{Auth: s.connectorAuth, Sources: s.connectorSources()}
	switch r.URL.Path {
	case "/api/webapp/connector/list":
		var req struct{}
		if !decodeWebappRequest(w, r, &req) {
			return
		}
		items := []map[string]any{}
		ids := make([]string, 0, len(scope.Operations))
		for id := range scope.Operations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			pkg, e := s.connectorSources().Load(id)
			if e != nil {
				continue
			}
			catalog, e := service.Describe(scope, id)
			if e != nil {
				continue
			}
			items = append(items, map[string]any{"connectorId": id, "name": pkg.Name, "packageVersion": pkg.Version, "operationCount": len(catalog.Operations)})
		}
		writeJSON(w, 200, api.Success(map[string]any{"items": items}))
	case "/api/webapp/connector/describe":
		var req struct {
			ConnectorID string `json:"connectorId"`
		}
		if !decodeWebappRequest(w, r, &req) {
			return
		}
		catalog, e := service.Describe(scope, req.ConnectorID)
		if e != nil {
			writeWebappError(w, e)
			return
		}
		writeJSON(w, 200, api.Success(catalog))
	case "/api/webapp/connector/invoke":
		var req connectorops.Request
		if !decodeWebappRequest(w, r, &req) {
			return
		}
		result, e := service.Invoke(ctx, scope, req)
		if e != nil {
			writeWebappError(w, e)
			return
		}
		writeJSON(w, 200, api.Success(result))
	default:
		writeWebappError(w, &connectorops.Error{Code: "not_found", Status: 404})
	}
}
func decodeWebappRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, connectorops.MaxJSONBytes)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		writeWebappError(w, &connectorops.Error{Code: "invalid_arguments", Status: 400})
		return false
	}
	return true
}
func writeWebappError(w http.ResponseWriter, err error) {
	code, status := "connector_unavailable", 503
	var operation *connectorops.Error
	if errors.As(err, &operation) {
		code, status = operation.Code, operation.Status
	} else if errors.Is(err, webapp.ErrDenied) {
		code, status = "app_grant_required", 403
	}
	writeJSON(w, status, map[string]any{"code": status, "msg": code, "data": map[string]any{"errorCode": code}})
}
