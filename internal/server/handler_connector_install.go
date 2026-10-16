package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
)

func (s *Server) handleConnectorImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, connector.MaxArchiveUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			s.writeConnectorError(w, connector.ErrArchiveTooLarge)
		} else {
			s.writeConnectorError(w, errors.New("invalid multipart upload"))
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	expectedID, err := connectorImportExpectedField(r.MultipartForm.Value, "expectedId")
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	expectedVersion, err := connectorImportExpectedField(r.MultipartForm.Value, "expectedVersion")
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	overwrite := false
	if raw := r.FormValue("overwrite"); raw != "" {
		var err error
		overwrite, err = strconv.ParseBool(raw)
		if err != nil {
			s.writeConnectorError(w, errors.New("overwrite must be boolean"))
			return
		}
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 || !strings.EqualFold(filepath.Ext(files[0].Filename), ".zip") {
		s.writeConnectorError(w, errors.New("upload exactly one ZIP in file"))
		return
	}
	f, err := files[0].Open()
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	defer f.Close()
	prepared, err := connector.PrepareArchive(r.Context(), s.connectorSources(), f, files[0].Size)
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	defer prepared.Close()
	if err := prepared.ValidateExpectedIdentity(expectedID, expectedVersion); err != nil {
		s.writeConnectorError(w, err)
		return
	}
	pkg, err := s.publishPreparedConnector(r.Context(), prepared, overwrite)
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeConnectorInstall(w, pkg)
}

// publishPreparedConnector is the single publication boundary shared by ZIP and
// HTTP MCP imports: connector operation lock, package revalidation and reload.
func (s *Server) publishPreparedConnector(ctx context.Context, prepared *connector.PreparedArchive, overwrite bool) (connector.Package, error) {
	return withCatalogDirectoryTransaction(ctx, s, "connectors", func(ctx context.Context) (connector.Package, error) {
		return prepared.Publish(ctx, overwrite, mcp.ValidateConnectorPackages, func() error {
			if s.deps.CatalogReloader != nil {
				return s.deps.CatalogReloader.Reload(context.WithoutCancel(ctx), "connectors")
			}
			return nil
		})
	})
}

func (s *Server) writeConnectorInstall(w http.ResponseWriter, pkg connector.Package) {
	response := map[string]any{"id": pkg.ID, "name": pkg.Manifest.Localized(responseLocale(w)).Name, "version": pkg.Version, "installed": true, "authMode": pkg.AuthMode}
	if pkg.CLI != nil {
		prepared, err := s.connectorAuth.StartPreparation(pkg.ID)
		if err != nil {
			response["preparation"] = map[string]any{"connectorId": pkg.ID, "status": "failed", "message": err.Error()}
		} else {
			response["preparation"] = prepared
		}
	}
	s.writeAgentHTTPResponse(w, response, nil)
}

func connectorImportExpectedField(fields map[string][]string, name string) (string, error) {
	values, present := fields[name]
	if !present {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" {
		return "", errors.New(name + " must be supplied once with a nonempty value")
	}
	return values[0], nil
}

func (s *Server) handleConnectorAuth(w http.ResponseWriter, r *http.Request) {
	s.handleConnectorAuthWithManager(w, r, s.connectorAuth)
}

func (s *Server) handleConnectorAuthWithManager(w http.ResponseWriter, r *http.Request, manager *connectorauth.Manager) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !connector.ValidID(id) {
		s.writeConnectorError(w, errors.New("connector id is required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		result, err := manager.StatusComponent(r.Context(), id, r.URL.Query().Get("component"))
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodPost:
		result, err := manager.StartComponent(id, r.URL.Query().Get("component"))
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Credentials map[string]string              `json:"credentials"`
			OAuthClient *connectorauth.OAuthClientInfo `json:"oauthClient,omitempty"`
		}
		data, readErr := io.ReadAll(r.Body)
		r.Body.Close()
		if readErr != nil || connector.DecodeJSON(data, &req) != nil {
			s.writeConnectorError(w, errors.New("invalid connector credentials request"))
			return
		}
		var result connectorauth.Session
		var err error
		if req.OAuthClient != nil {
			if req.Credentials != nil {
				s.writeConnectorError(w, errors.New("choose credentials or oauthClient"))
				return
			}
			result, err = manager.SetOAuthClient(r.Context(), id, r.URL.Query().Get("component"), *req.OAuthClient)
		} else {
			result, err = manager.SetToken(r.Context(), id, req.Credentials)
		}
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodDelete:
		if r.URL.Query().Get("component") != "" {
			s.writeConnectorError(w, errors.New("logout clears the entire connector; omit component"))
			return
		}
		if err := manager.Logout(r.Context(), id); err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, map[string]any{"id": id, "status": "unauthorized"}, nil)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, DELETE")
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(405, "method_not_allowed", "method not allowed"))
	}
}

func (s *Server) handleConnectorAuthCancel(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !connector.ValidID(id) {
		s.writeConnectorError(w, errors.New("connector id is required"))
		return
	}
	if err := s.connectorAuth.CancelSession(id, r.URL.Query().Get("sessionId")); err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, map[string]any{"id": id, "status": "canceled"}, nil)
}

func (s *Server) writeConnectorError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "invalid_connector"
	var inUse *adminsource.ConnectorInUseError
	if errors.As(err, &inUse) {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusErrorWithData(http.StatusConflict, "connector_in_use", err.Error(), map[string]any{"agentKeys": inUse.AgentKeys}))
		return
	}
	switch {
	case errors.Is(err, connector.ErrArchiveIdentityMismatch):
		status, code = http.StatusConflict, "connector_identity_mismatch"
	case errors.Is(err, connectorauth.ErrOAuthClientSetupRequired):
		status, code = http.StatusConflict, "connector_setup_required"
	case errors.Is(err, connectorauth.ErrOAuthAuthorizationRequired), errors.Is(err, connectorauth.ErrTokenRejected):
		status, code = http.StatusUnauthorized, "connector_auth_required"
	case errors.Is(err, connectorauth.ErrOAuthRefreshFailed), errors.Is(err, connectorauth.ErrOAuthCredentialUnavailable), errors.Is(err, connectorauth.ErrCredentialCheckFailed), errors.Is(err, connectorauth.ErrCredentialValidatorUnavailable):
		status, code = http.StatusServiceUnavailable, "connector_execution_failed"
	case errors.Is(err, connectorauth.ErrPreparationRequired):
		status, code = http.StatusConflict, "connector_preparation_required"
	case errors.Is(err, connectorauth.ErrAuthNotRequired):
		status, code = http.StatusConflict, "connector_auth_not_required"
	case errors.Is(err, connector.ErrPackageNotFound):
		status, code = http.StatusNotFound, "connector_not_found"
	case errors.Is(err, connector.ErrDeleteReload):
		status, code = http.StatusInternalServerError, "connector_reload_failed"
	case errors.Is(err, connector.ErrBusy):
		status, code = http.StatusConflict, "connector_busy"
	case errors.Is(err, connector.ErrBuiltinReadOnly):
		status, code = http.StatusForbidden, "builtin_connector_readonly"
	case errors.Is(err, connector.ErrPackageExists):
		status, code = http.StatusConflict, "connector_exists"
	case errors.Is(err, connector.ErrArchiveTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "payload_too_large"
	}
	s.writeAgentHTTPResponse(w, nil, newAgentStatusError(status, code, err.Error()))
}

func (s *Server) handleConnectorPrepare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !connector.ValidID(id) {
		s.writeConnectorError(w, errors.New("connector id is required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		result, err := s.connectorAuth.PreparationStatus(id)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodPost:
		result, err := s.connectorAuth.StartPreparation(id)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodDelete:
		if err := s.connectorAuth.CancelPreparation(id); err != nil {
			s.writeConnectorError(w, err)
			return
		}
		result, err := s.connectorAuth.PreparationStatus(id)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(405, "method_not_allowed", "method not allowed"))
	}
}
