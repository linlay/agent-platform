package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/connectorops"
)

// Only an authenticated local authority can issue execution grants or manage
// credentials. Existing JWT scope=app and device claims identify that authority;
// no caller application identity is accepted from the payload.
func connectorAuthoritySubject(r *http.Request) (string, error) {
	p := PrincipalFromContext(r.Context())
	if !isDesktopAppPrincipal(p) {
		return "", connectorops.ErrDenied
	}
	return p.Subject, nil
}
func (s *Server) handleConnectorExecutionGrant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	subject, err := connectorAuthoritySubject(r)
	if err != nil {
		writeConnectorExecutionError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		err = s.connectorGrants.Revoke(subject, r.URL.Query().Get("grantId"))
		if err != nil {
			writeConnectorExecutionError(w, err)
			return
		}
		writeJSON(w, 200, api.Success(map[string]bool{"revoked": true}))
		return
	}
	if r.Method != http.MethodPost {
		writeConnectorExecutionError(w, &connectorops.Error{Code: "method_not_allowed", Status: 405})
		return
	}
	var req struct {
		Version              int                       `json:"version"`
		IdempotencyNamespace string                    `json:"idempotencyNamespace"`
		Execution            []connectorops.Permission `json:"execution"`
	}
	if !decodeBoundedRequest(w, r, &req) {
		return
	}
	if req.Version != 3 {
		writeConnectorExecutionError(w, &connectorops.Error{Code: "connector_contract_upgrade_required", Status: 409})
		return
	}
	grant, err := s.connectorGrants.Issue(subject, req.IdempotencyNamespace, req.Execution)
	if err != nil {
		writeConnectorExecutionError(w, err)
		return
	}
	writeJSON(w, 200, api.Success(grant))
}
func (s *Server) handleTrustedConnectorAuth(w http.ResponseWriter, r *http.Request) {
	if _, err := connectorAuthoritySubject(r); err != nil {
		writeConnectorExecutionError(w, err)
		return
	}
	manager := s.connectorAuth
	// Credential management is separate from execution grants. Manager owns
	// deduplication, credential fences and expiry.

	if strings.HasSuffix(r.URL.Path, "/cancel") {
		if r.Method != http.MethodPost || r.URL.Query().Get("sessionId") == "" {
			writeConnectorExecutionError(w, &connectorops.Error{Code: "invalid_arguments", Status: 400})
			return
		}
		err := manager.CancelSession(r.URL.Query().Get("id"), r.URL.Query().Get("sessionId"))
		if err != nil {
			writeConnectorExecutionError(w, &connectorops.Error{Code: "auth_session_unavailable", Status: 409})
			return
		}
		writeJSON(w, 200, api.Success(map[string]any{"status": "canceled"}))
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("sessionId") != "" {
		result, err := manager.SessionStatus(r.URL.Query().Get("id"), r.URL.Query().Get("sessionId"))
		if err != nil {
			writeConnectorExecutionError(w, &connectorops.Error{Code: "auth_session_unavailable", Status: 409})
			return
		}
		writeJSON(w, 200, api.Success(result))
		return
	}
	s.handleConnectorAuthWithManager(w, r, manager)
}
func (s *Server) handleConnectorExecution(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeConnectorExecutionError(w, &connectorops.Error{Code: "method_not_allowed", Status: 405})
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer cxg_") {
		writeConnectorExecutionError(w, connectorops.ErrDenied)
		return
	}
	scope, grantCtx, err := s.connectorGrants.Scope(strings.TrimPrefix(authorization, "Bearer "))
	if err != nil {
		writeConnectorExecutionError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(grantCtx, cancel)
	defer stop()
	service := connectorops.Service{Auth: s.connectorAuth, Sources: s.connectorSources()}
	switch r.URL.Path {
	case "/api/connectors/execution/list":
		var req struct{}
		if !decodeBoundedRequest(w, r, &req) {
			return
		}
		items := []map[string]any{}
		ids := make([]string, 0, len(scope.Execution))
		seen := map[string]bool{}
		for _, p := range scope.Execution {
			if !seen[p.ConnectorID] {
				ids = append(ids, p.ConnectorID)
				seen[p.ConnectorID] = true
			}
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
			items = append(items, map[string]any{"connectorId": id, "name": pkg.Name, "packageVersion": pkg.Version, "adapters": catalog.Adapters})
		}
		writeJSON(w, 200, api.Success(map[string]any{"items": items}))
	case "/api/connectors/execution/describe":
		var req struct {
			ConnectorID string `json:"connectorId"`
		}
		if !decodeBoundedRequest(w, r, &req) {
			return
		}
		catalog, e := service.Describe(scope, req.ConnectorID)
		if e != nil {
			writeConnectorExecutionError(w, e)
			return
		}
		writeJSON(w, 200, api.Success(catalog))
	case "/api/connectors/execution/invoke":
		var req connectorops.Request
		if !decodeBoundedRequest(w, r, &req) {
			return
		}
		result, e := service.Invoke(ctx, scope, req)
		if e != nil {
			writeConnectorExecutionError(w, e)
			return
		}
		writeJSON(w, 200, api.Success(result))
	default:
		writeConnectorExecutionError(w, &connectorops.Error{Code: "not_found", Status: 404})
	}
}
func writeConnectorExecutionError(w http.ResponseWriter, err error) {
	code, status := "connector_unavailable", 503
	var operation *connectorops.Error
	if errors.As(err, &operation) {
		code, status = operation.Code, operation.Status
	} else if errors.Is(err, connectorops.ErrDenied) {
		code, status = "connector_grant_required", 403
	}
	writeJSON(w, status, map[string]any{"code": status, "msg": code, "data": map[string]any{"errorCode": code}})
}
