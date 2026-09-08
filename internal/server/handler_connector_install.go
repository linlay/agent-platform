package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"agent-platform/internal/connector"
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
	pkg, err := connector.ImportArchive(r.Context(), s.connectorSources(), f, files[0].Size, overwrite, mcp.ValidateConnectorPackages, func() error {
		if s.deps.CatalogReloader != nil {
			return s.deps.CatalogReloader.Reload(context.WithoutCancel(r.Context()), "connectors")
		}
		return nil
	})
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, map[string]any{"id": pkg.ID, "name": pkg.Name, "version": pkg.Version, "installed": true, "authMode": pkg.AuthMode}, nil)
}

func (s *Server) handleConnectorAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !connector.ValidID(id) {
		s.writeConnectorError(w, errors.New("connector id is required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		result, err := s.connectorAuth.Status(r.Context(), id)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodPost:
		result, err := s.connectorAuth.Start(id)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Credentials map[string]string `json:"credentials"`
		}
		data, readErr := io.ReadAll(r.Body)
		r.Body.Close()
		if readErr != nil || connector.DecodeJSON(data, &req) != nil {
			s.writeConnectorError(w, errors.New("invalid connector credentials request"))
			return
		}
		result, err := s.connectorAuth.SetToken(r.Context(), id, req.Credentials)
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
	case http.MethodDelete:
		if err := s.connectorAuth.Logout(r.Context(), id); err != nil {
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
	if err := s.connectorAuth.Cancel(id); err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, map[string]any{"id": id, "status": "canceled"}, nil)
}

func (s *Server) writeConnectorError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "invalid_connector"
	switch {
	case errors.Is(err, connector.ErrBuiltinReadOnly):
		status, code = http.StatusForbidden, "builtin_connector_readonly"
	case errors.Is(err, connector.ErrPackageExists):
		status, code = http.StatusConflict, "connector_exists"
	case errors.Is(err, connector.ErrArchiveTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "payload_too_large"
	}
	s.writeAgentHTTPResponse(w, nil, newAgentStatusError(status, code, err.Error()))
}
